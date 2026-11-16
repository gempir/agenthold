package preview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Limits and lifetimes.
const (
	CodeTTL    = 60 * time.Second
	SessionTTL = 24 * time.Hour
	// NameCooldown is how long a freed name stays reserved for its owner.
	NameCooldown = 7 * 24 * time.Hour
	// MaxPerAgent bounds the exposures of one machine.
	MaxPerAgent = 20
	// sessionsPerExposure bounds a user's live preview sessions per exposure
	// (oldest evicted).
	sessionsPerExposure = 10
	cacheTTL            = 30 * time.Second
	cacheMax            = 10000
)

// Errors.
var (
	ErrDisabled  = errors.New("preview URLs are not configured on this server (preview.host_suffix)")
	ErrNotFound  = errors.New("no such exposure")
	ErrNameTaken = errors.New("this name is taken")
	ErrPortTaken = errors.New("this port is already exposed")
	ErrTooMany   = fmt.Errorf("a machine can expose at most %d ports", MaxPerAgent)
	ErrBadCode   = errors.New("the sign-in link expired, was already used or was opened in another browser; reload the page")
)

// Service manages exposures and the gate's codes, sessions and consents.
type Service struct {
	Store *store.Store
	Hosts Hosts
	Now   func() time.Time

	mu    sync.Mutex
	cache map[string]cached // positive lookups only
}

type cached struct {
	e  db.Exposure
	at time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Enabled reports whether preview URLs are configured.
func (s *Service) Enabled() bool { return s != nil && s.Hosts.Enabled() }

// URL is an exposure's URL.
func (s *Service) URL(e db.Exposure) string { return s.Hosts.URL(e.Name) }

// Lookup finds an exposure by name. Hits are cached briefly (every write
// through this service clears the cache); misses are not cached, so a flood
// of unknown names costs one indexed query each and evicts nothing.
func (s *Service) Lookup(ctx context.Context, name string) (db.Exposure, error) {
	now := s.now()
	s.mu.Lock()
	c, ok := s.cache[name]
	s.mu.Unlock()
	if ok && now.Sub(c.at) < cacheTTL {
		return c.e, nil
	}
	e, err := s.Store.GetExposureByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Exposure{}, ErrNotFound
	}
	if err != nil {
		return db.Exposure{}, err
	}
	s.mu.Lock()
	if s.cache == nil || len(s.cache) >= cacheMax {
		s.cache = map[string]cached{}
	}
	s.cache[name] = cached{e: e, at: now}
	s.mu.Unlock()
	return e, nil
}

// Invalidate drops cached lookups (after writes, or an agent's deletion).
func (s *Service) Invalidate() {
	s.mu.Lock()
	clear(s.cache)
	s.mu.Unlock()
}

// List returns an agent's exposures by port.
func (s *Service) List(ctx context.Context, agentID string) ([]db.Exposure, error) {
	return s.Store.ListExposures(ctx, agentID)
}

// Get returns one of an agent's exposures.
func (s *Service) Get(ctx context.Context, agentID, exposureID string) (db.Exposure, error) {
	e, err := s.Store.GetExposure(ctx, db.GetExposureParams{ID: exposureID, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ByPort returns the exposure of an agent's port.
func (s *Service) ByPort(ctx context.Context, agentID string, port int) (db.Exposure, error) {
	e, err := s.Store.GetExposureByPort(ctx, db.GetExposureByPortParams{AgentID: agentID, Port: int64(port)})
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// CreateParams describe a new exposure.
type CreateParams struct {
	Agent db.Agent
	Port  int
	// Name is optional: the default is <agent-name>-<port>, with a random
	// suffix when that is taken. An explicit name must be free.
	Name          string
	Access        Access // default Private
	WakeOnRequest bool
	CreatedBy     string
}

// nameFree reports whether ownerID may use name now: nobody else released
// it within the cooldown. (A live exposure with the name is caught by the
// unique constraint.)
func (s *Service) nameFree(ctx context.Context, name, ownerID string) (bool, error) {
	r, err := s.Store.GetReleasedExposureName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return r.OwnerID == ownerID || s.now().Sub(time.Unix(r.ReleasedAt, 0)) >= NameCooldown, nil
}

// Create exposes a port.
func (s *Service) Create(ctx context.Context, p CreateParams) (db.Exposure, error) {
	if !s.Enabled() {
		return db.Exposure{}, ErrDisabled
	}
	if p.Port < 1 || p.Port > 65535 {
		return db.Exposure{}, Invalid("port must be between 1 and 65535")
	}
	if p.Access == "" {
		p.Access = Private
	}
	if !p.Access.Valid() {
		return db.Exposure{}, Invalid("access must be private, authenticated or public")
	}
	if _, err := s.ByPort(ctx, p.Agent.ID, p.Port); err == nil {
		return db.Exposure{}, ErrPortTaken
	} else if !errors.Is(err, ErrNotFound) {
		return db.Exposure{}, err
	}
	if n, err := s.Store.CountExposures(ctx, p.Agent.ID); err != nil {
		return db.Exposure{}, err
	} else if n >= MaxPerAgent {
		return db.Exposure{}, ErrTooMany
	}
	explicit := p.Name != ""
	name := strings.TrimSpace(p.Name)
	if !explicit {
		name = DefaultName(p.Agent.Name, p.Port)
	} else if err := ValidName(name); err != nil {
		return db.Exposure{}, err
	}
	e := db.Exposure{
		ID: id.New("exp"), AgentID: p.Agent.ID, Port: int64(p.Port), Access: string(p.Access),
		WakeOnRequest: b2i(p.WakeOnRequest), CreatedBy: p.CreatedBy, CreatedAt: s.now().Unix(),
	}
	defer s.Invalidate()
	for attempt := 0; ; attempt++ {
		e.Name = name
		if attempt > 0 {
			r := "-" + id.Random(3)[:4]
			e.Name = trimSlug(name, config.MaxExposureName-len(r)) + r
		}
		free, err := s.nameFree(ctx, e.Name, p.Agent.OwnerID)
		if err != nil {
			return db.Exposure{}, err
		}
		if free {
			err = s.Store.Tx(ctx, func(q *db.Queries) error {
				if err := q.CreateExposure(ctx, db.CreateExposureParams(e)); err != nil {
					return err
				}
				return q.ClaimExposureName(ctx, e.Name)
			})
			switch {
			case err == nil:
				return e, nil
			case !store.IsUniqueViolation(err):
				return db.Exposure{}, err
			}
			// The port raced with another request, or the name is taken.
			if _, perr := s.ByPort(ctx, p.Agent.ID, p.Port); perr == nil {
				return db.Exposure{}, ErrPortTaken
			}
		}
		if explicit {
			return db.Exposure{}, ErrNameTaken
		}
		if attempt >= 5 {
			return db.Exposure{}, ErrNameTaken
		}
	}
}

// Patch changes an exposure; nil fields stay.
type Patch struct {
	Name          string // "" keeps the name
	Access        Access // "" keeps the access
	WakeOnRequest *bool
}

// Update renames an exposure or changes its settings. ownerID owns its machine.
func (s *Service) Update(ctx context.Context, e db.Exposure, ownerID string, p Patch) (db.Exposure, error) {
	old := e.Name
	if p.Name != "" && p.Name != e.Name {
		if err := ValidName(p.Name); err != nil {
			return e, err
		}
		if free, err := s.nameFree(ctx, p.Name, ownerID); err != nil {
			return e, err
		} else if !free {
			return e, ErrNameTaken
		}
		e.Name = p.Name
	}
	if p.Access != "" {
		if !p.Access.Valid() {
			return e, Invalid("access must be private, authenticated or public")
		}
		e.Access = string(p.Access)
	}
	if p.WakeOnRequest != nil {
		e.WakeOnRequest = b2i(*p.WakeOnRequest)
	}
	defer s.Invalidate()
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateExposure(ctx, db.UpdateExposureParams{Name: e.Name, Access: e.Access, WakeOnRequest: e.WakeOnRequest, ID: e.ID}); err != nil {
			return err
		}
		if old == e.Name {
			return nil
		}
		if err := q.ClaimExposureName(ctx, e.Name); err != nil {
			return err
		}
		return q.ReleaseExposureName(ctx, db.ReleaseExposureNameParams{Name: old, OwnerID: ownerID, ReleasedAt: s.now().Unix()})
	})
	if store.IsUniqueViolation(err) {
		return e, ErrNameTaken
	}
	return e, err
}

// Delete removes an exposure; its name stays reserved for ownerID for the cooldown.
func (s *Service) Delete(ctx context.Context, e db.Exposure, ownerID string) error {
	defer s.Invalidate()
	return s.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteExposure(ctx, e.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return q.ReleaseExposureName(ctx, db.ReleaseExposureNameParams{Name: e.Name, OwnerID: ownerID, ReleasedAt: s.now().Unix()})
	})
}

// DeleteAgent removes all exposures of a machine that is being deleted.
func (s *Service) DeleteAgent(ctx context.Context, ag db.Agent) error {
	defer s.Invalidate()
	rows, err := s.List(ctx, ag.ID)
	if err != nil {
		return err
	}
	for _, e := range rows {
		if err := s.Delete(ctx, e, ag.OwnerID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// IssueCode creates a single-use code that proves userID to one exposure,
// for the browser whose state cookie hashes to stateHash.
func (s *Service) IssueCode(ctx context.Context, userID string, e db.Exposure, stateHash string) (string, error) {
	if stateHash == "" {
		return "", errors.New("missing state")
	}
	code := id.Random(32)
	err := s.Store.CreatePreviewCode(ctx, db.CreatePreviewCodeParams{
		CodeHash: Hash(code), UserID: userID, ExposureID: e.ID, Host: s.Hosts.Host(e.Name),
		StateHash: stateHash, ExpiresAt: s.now().Add(CodeTTL).Unix(),
	})
	return code, err
}

// TakeCode consumes a code and returns its user id. A code works once, for
// its exposure and host, in the browser holding the state it was issued
// for, until it expires. Any attempt burns it.
func (s *Service) TakeCode(ctx context.Context, code string, e db.Exposure, state string) (string, error) {
	if code == "" || state == "" {
		return "", ErrBadCode
	}
	row, err := s.Store.TakePreviewCode(ctx, db.TakePreviewCodeParams{CodeHash: Hash(code), ExpiresAt: s.now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadCode
	}
	if err != nil {
		return "", err
	}
	if row.ExposureID != e.ID || row.Host != s.Hosts.Host(e.Name) || row.StateHash != Hash(state) {
		return "", ErrBadCode
	}
	return row.UserID, nil
}

// CreateSession starts a preview session of userID on an exposure. A user
// keeps at most a few sessions per exposure; older ones are dropped.
func (s *Service) CreateSession(ctx context.Context, userID string, e db.Exposure) (string, error) {
	now := s.now()
	token := id.Random(32)
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreatePreviewSession(ctx, db.CreatePreviewSessionParams{
			TokenHash: Hash(token), UserID: userID, ExposureID: e.ID, Host: s.Hosts.Host(e.Name),
			CreatedAt: now.Unix(), ExpiresAt: now.Add(SessionTTL).Unix(),
		}); err != nil {
			return err
		}
		return q.TrimPreviewSessions(ctx, db.TrimPreviewSessionsParams{UserID: userID, ExposureID: e.ID})
	})
	return token, err
}

// SessionUser returns the user of a preview session. It only works for the
// exposure (and host) it was created for: a deleted, renamed or reclaimed
// name never accepts old cookies.
func (s *Service) SessionUser(ctx context.Context, token string, e db.Exposure) (*db.User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	row, err := s.Store.GetPreviewSessionUser(ctx, db.GetPreviewSessionUserParams{
		TokenHash: Hash(token), ExposureID: e.ID, Host: s.Hosts.Host(e.Name), ExpiresAt: s.now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row.User, nil
}

// EndSession deletes a preview session.
func (s *Service) EndSession(ctx context.Context, token string) error {
	return s.Store.DeletePreviewSession(ctx, Hash(token))
}

// EndUserSessions deletes all of a user's preview sessions (main logout).
func (s *Service) EndUserSessions(ctx context.Context, userID string) error {
	return s.Store.DeleteUserPreviewSessions(ctx, userID)
}

// Consented reports whether userID agreed to show their identity to e.
func (s *Service) Consented(ctx context.Context, userID string, e db.Exposure) (bool, error) {
	n, err := s.Store.HasPreviewConsent(ctx, db.HasPreviewConsentParams{UserID: userID, ExposureID: e.ID})
	return n > 0, err
}

// Consent records that userID agreed to show their identity to e.
func (s *Service) Consent(ctx context.Context, userID string, e db.Exposure) error {
	return s.Store.CreatePreviewConsent(ctx, db.CreatePreviewConsentParams{UserID: userID, ExposureID: e.ID, CreatedAt: s.now().Unix()})
}

// Sweep deletes expired codes, sessions and name reservations.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now()
	return errors.Join(
		s.Store.DeleteExpiredPreviewCodes(ctx, now.Unix()),
		s.Store.DeleteExpiredPreviewSessions(ctx, now.Unix()),
		s.Store.DeleteOldReleasedExposureNames(ctx, now.Add(-NameCooldown).Unix()),
	)
}

// Run sweeps periodically until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil && log != nil {
				log.Warn("preview sweep", "err", err)
			}
		}
	}
}

// Hash is the stored form of the gate's secrets (codes, cookies, states).
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
