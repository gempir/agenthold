// Package slack connects agenthold to a Slack workspace: people chat with
// their agents in DMs and channel threads, as their linked agenthold user.
//
// The app uses Socket Mode only (an outbound WebSocket: no public endpoint,
// no signing secret). Everything a Slack user does runs through the API's
// in-process handlers as the linked user, so permissions, validation,
// waking and audit are the API's. What a thread shows comes from the bound
// session's event log, whoever prompted it (Slack, the web UI, a schedule,
// a webhook or another agent).
package slack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Options configure the service.
type Options struct {
	Store    *store.Store
	Box      *secrets.Box
	API      gen.StrictServerInterface
	Auth     *auth.Service
	Sessions *session.Manager
	// PublicURL is the web UI's base URL (links to chats and the link page).
	PublicURL string
	Log       *slog.Logger
	// APIURL replaces Slack's Web API base URL (tests); it ends with "/".
	APIURL string
	// HTTPClient makes the Web API requests (default http.DefaultClient).
	HTTPClient *http.Client
	// NoSocket skips the Socket Mode connection; tests call the Handle
	// functions directly.
	NoSocket bool
	// Throttle is the least time between two updates of one message
	// (default one second).
	Throttle time.Duration
	Now      func() time.Time
}

// threadKey identifies a Slack thread.
type threadKey struct{ team, channel, ts string }

func (k threadKey) String() string { return k.team + "/" + k.channel + "/" + k.ts }

// installation is the connected app (without its tokens).
type installation struct {
	teamID, teamName, botUserID, botID string
	updatedAt                          time.Time
}

// Service is the Slack integration. It implements api.Slack.
type Service struct {
	o      Options
	lg     *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// events carries appended session events of bound sessions to the
	// dispatcher; overflow makes it poke every thread instead.
	events   chan observed
	overflow chan struct{}

	mu        sync.Mutex
	closed    bool
	client    *slackgo.Client // nil while not configured
	inst      installation
	stopConn  context.CancelFunc // stops the Socket Mode connection
	connDone  chan struct{}
	connected bool
	lastErr   string
	// bound holds the ids of sessions bound to at least one thread.
	bound   map[string]bool
	keys    map[string][]threadKey // session id → threads (cache)
	threads map[threadKey]*thread  // running thread workers
	picks   map[string]*pick       // messages waiting for an agent choice
	// sent are prompts Slack sent and has not seen in the log yet, so a
	// thread does not repeat its own messages.
	sent   []sentPrompt
	claims map[claimKey]threadKey
}

type observed struct {
	sessionID string
	seq       int64
}

type sentPrompt struct {
	sessionID, userID, text string
	from                    threadKey
	at                      time.Time
}

type claimKey struct {
	sessionID string
	seq       int64
}

var _ api.Slack = (*Service)(nil)

// New returns the service and registers it as an observer of the session
// log. Start connects it.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Throttle == 0 {
		o.Throttle = time.Second
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		o: o, lg: o.Log.With("component", "slack"), ctx: ctx, cancel: cancel,
		events: make(chan observed, 4096), overflow: make(chan struct{}, 1),
		bound: map[string]bool{}, keys: map[string][]threadKey{}, threads: map[threadKey]*thread{},
		picks: map[string]*pick{}, claims: map[claimKey]threadKey{},
	}
	if o.Sessions != nil {
		o.Sessions.Observe(s.observe)
	}
	s.wg.Add(1)
	go s.dispatch()
	return s
}

// Start loads the stored installation and connects to Slack.
func (s *Service) Start(ctx context.Context) error {
	ids, err := s.o.Store.ListSlackThreadSessions(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, id := range ids {
		s.bound[id] = true
	}
	s.mu.Unlock()
	row, err := s.o.Store.GetSlackInstallation(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	bot, app, err := s.openTokens(row)
	if err != nil {
		s.setStatus(false, err.Error())
		return nil // a wrong master key must not stop the server
	}
	s.use(row, bot, app)
	return nil
}

// Close disconnects and waits for every goroutine of the service.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	stop, done := s.stopConn, s.connDone
	s.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	s.cancel()
	s.wg.Wait()
}

// goTracked runs fn in a goroutine that Close waits for.
func (s *Service) goTracked(fn func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

func (s *Service) now() time.Time { return s.o.Now() }

func (s *Service) newClient(bot, app string) *slackgo.Client {
	opts := []slackgo.Option{slackgo.OptionHTTPClient(s.o.HTTPClient), slackgo.OptionRetry(3)}
	if app != "" {
		opts = append(opts, slackgo.OptionAppLevelToken(app))
	}
	if s.o.APIURL != "" {
		opts = append(opts, slackgo.OptionAPIURL(s.o.APIURL))
	}
	return slackgo.New(bot, opts...)
}

// api returns the bot client, or nil while Slack is not configured.
func (s *Service) api() *slackgo.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Service) installation() installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inst
}

func (s *Service) setStatus(connected bool, lastErr string) {
	s.mu.Lock()
	s.connected = connected
	if lastErr != "" || connected {
		s.lastErr = lastErr
	}
	s.mu.Unlock()
}

var (
	aadBot = []byte("slack:bot")
	aadApp = []byte("slack:app")
)

func (s *Service) openTokens(row db.SlackInstallation) (string, string, error) {
	bot, err := s.o.Box.Open(row.BotToken, aadBot)
	if err != nil {
		return "", "", fmt.Errorf("cannot decrypt the Slack bot token: %w", err)
	}
	app, err := s.o.Box.Open(row.AppToken, aadApp)
	if err != nil {
		return "", "", fmt.Errorf("cannot decrypt the Slack app token: %w", err)
	}
	return string(bot), string(app), nil
}

// use switches to an installation and (re)starts its connection.
func (s *Service) use(row db.SlackInstallation, bot, app string) {
	s.disconnect()
	client := s.newClient(bot, app)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.client = client
	s.inst = installation{teamID: row.TeamID, teamName: row.TeamName, botUserID: row.BotUserID, botID: row.BotID, updatedAt: time.Unix(row.UpdatedAt, 0).UTC()}
	s.connected, s.lastErr = false, ""
	if s.o.NoSocket {
		return
	}
	ctx, stop := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.stopConn, s.connDone = stop, done
	go func() {
		defer close(done)
		s.runSocket(ctx, client)
	}()
}

// disconnect stops the Socket Mode connection, if any.
func (s *Service) disconnect() {
	s.mu.Lock()
	stop, done := s.stopConn, s.connDone
	s.stopConn, s.connDone = nil, nil
	s.client = nil
	s.connected = false
	s.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Settings implements api.Slack.
func (s *Service) Settings(ctx context.Context) (gen.SlackSettings, error) {
	out := gen.SlackSettings{}
	row, err := s.o.Store.GetSlackInstallation(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	connected, lastErr := s.connected, s.lastErr
	if s.o.NoSocket && s.client != nil {
		connected = true
	}
	s.mu.Unlock()
	updated := time.Unix(row.UpdatedAt, 0).UTC()
	out.Configured, out.Enabled, out.Connected = true, row.Enabled == 1, connected
	out.TeamId, out.TeamName, out.BotUserId, out.UpdatedAt = &row.TeamID, &row.TeamName, &row.BotUserID, &updated
	if lastErr != "" {
		out.LastError = &lastErr
	}
	return out, nil
}

// Configure implements api.Slack: it checks both tokens with Slack, stores
// them and connects.
func (s *Service) Configure(ctx context.Context, userID, bot, app string) (gen.SlackSettings, error) {
	c := s.newClient(bot, app)
	at, err := c.AuthTestContext(ctx)
	if err != nil {
		return gen.SlackSettings{}, api.NewError(http.StatusBadRequest, "Slack refused the bot token: "+slackError(err))
	}
	if at.BotID == "" || at.UserID == "" {
		return gen.SlackSettings{}, api.NewError(http.StatusBadRequest, "this is not a bot token (xoxb-…): auth.test returned no bot")
	}
	if _, _, err := c.StartSocketModeContext(ctx); err != nil {
		return gen.SlackSettings{}, api.NewError(http.StatusBadRequest, "Slack refused the app-level token: "+slackError(err)+" (it needs the connections:write scope, and Socket Mode must be on)")
	}
	sealedBot, err := s.o.Box.Seal([]byte(bot), aadBot)
	if err != nil {
		return gen.SlackSettings{}, err
	}
	sealedApp, err := s.o.Box.Seal([]byte(app), aadApp)
	if err != nil {
		return gen.SlackSettings{}, err
	}
	row := db.SlackInstallation{
		BotToken: sealedBot, AppToken: sealedApp, TeamID: at.TeamID, TeamName: at.Team,
		BotUserID: at.UserID, BotID: at.BotID, UpdatedBy: userID, UpdatedAt: s.now().Unix(), Enabled: 1,
	}
	if err := s.o.Store.PutSlackInstallation(ctx, db.PutSlackInstallationParams{
		BotToken: row.BotToken, AppToken: row.AppToken, TeamID: row.TeamID, TeamName: row.TeamName,
		BotUserID: row.BotUserID, BotID: row.BotID, UpdatedBy: userID, UpdatedAt: row.UpdatedAt,
	}); err != nil {
		return gen.SlackSettings{}, err
	}
	s.lg.Info("slack app connected", "team", at.Team, "team_id", at.TeamID, "bot_user", at.UserID)
	s.use(row, bot, app)
	return s.Settings(ctx)
}

// Remove implements api.Slack.
func (s *Service) Remove(ctx context.Context) error {
	s.disconnect()
	s.mu.Lock()
	s.inst = installation{}
	s.lastErr = ""
	s.mu.Unlock()
	return s.o.Store.DeleteSlackInstallation(ctx)
}

// slackError is the error Slack returned, without the library's wrapping.
func slackError(err error) string {
	var se slackgo.SlackErrorResponse
	if errors.As(err, &se) {
		return se.Err
	}
	return err.Error()
}
