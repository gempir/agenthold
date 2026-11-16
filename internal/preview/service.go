package preview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Lifetimes of the gate's secrets.
const (
	CodeTTL    = 60 * time.Second
	SessionTTL = 24 * time.Hour
	cacheTTL   = 30 * time.Second
)

// Errors.
var (
	ErrDisabled  = errors.New("preview URLs are not configured on this server (preview.host_suffix)")
	ErrNotFound  = errors.New("no such exposure")
	ErrNameTaken = errors.New("this name is taken")
	ErrPortTaken = errors.New("this port is already exposed")
	ErrBadCode   = errors.New("the sign-in link expired or was already used; reload the page")
)

// Service manages exposures and the gate's codes and sessions.
type Service struct {
	Store *store.Store
	Hosts Hosts
	Now   func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	e  *db.Exposure // nil: no exposure with this name
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

// Lookup finds an exposure by name, through a short cache that every write
// through this service clears.
func (s *Service) Lookup(ctx context.Context, name string) (db.Exposure, error) {
	now := s.now()
	s.mu.Lock()
	c, ok := s.cache[name]
	s.mu.Unlock()
	if ok && now.Sub(c.at) < cacheTTL {
		if c.e == nil {
			return db.Exposure{}, ErrNotFound
		}
		return *c.e, nil
	}
	e, err := s.Store.GetExposureByName(ctx, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.Exposure{}, err
	}
	entry := cached{at: now}
	if err == nil {
		entry.e = &e
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	if len(s.cache) > 10000 {
		clear(s.cache)
	}
	s.cache[name] = entry
	s.mu.Unlock()
	if entry.e == nil {
		return db.Exposure{}, ErrNotFound
	}
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
	Name      string
	Access    Access // default Private
	CreatedBy string
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
	explicit := p.Name != ""
	name := strings.TrimSpace(p.Name)
	if !explicit {
		name = DefaultName(p.Agent.Name, p.Port)
	} else if err := ValidName(name); err != nil {
		return db.Exposure{}, err
	}
	e := db.Exposure{
		ID: id.New("exp"), AgentID: p.Agent.ID, Port: int64(p.Port), Access: string(p.Access),
		CreatedBy: p.CreatedBy, CreatedAt: s.now().Unix(),
	}
	defer s.Invalidate()
	for attempt := 0; ; attempt++ {
		e.Name = name
		if attempt > 0 {
			r := "-" + id.Random(3)[:4]
			e.Name = trimSlug(name, config.MaxExposureName-len(r)) + r
		}
		err := s.Store.CreateExposure(ctx, db.CreateExposureParams(e))
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
		if explicit {
			return db.Exposure{}, ErrNameTaken
		}
		if attempt >= 5 {
			return db.Exposure{}, err
		}
	}
}

// Update renames an exposure or changes its access. Empty values keep the
// current ones.
func (s *Service) Update(ctx context.Context, e db.Exposure, name string, access Access) (db.Exposure, error) {
	if name != "" {
		if err := ValidName(name); err != nil {
			return e, err
		}
		e.Name = name
	}
	if access != "" {
		if !access.Valid() {
			return e, Invalid("access must be private, authenticated or public")
		}
		e.Access = string(access)
	}
	defer s.Invalidate()
	err := s.Store.UpdateExposure(ctx, db.UpdateExposureParams{Name: e.Name, Access: e.Access, ID: e.ID})
	if store.IsUniqueViolation(err) {
		return e, ErrNameTaken
	}
	return e, err
}

// Delete removes an exposure.
func (s *Service) Delete(ctx context.Context, e db.Exposure) error {
	defer s.Invalidate()
	n, err := s.Store.DeleteExposure(ctx, e.ID)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// IssueCode creates a single-use code that proves userID to host.
func (s *Service) IssueCode(ctx context.Context, userID, host string) (string, error) {
	now := s.now()
	_ = s.Store.DeleteExpiredPreviewCodes(ctx, now.Unix())
	code := id.Random(32)
	err := s.Store.CreatePreviewCode(ctx, db.CreatePreviewCodeParams{
		CodeHash: hash(code), UserID: userID, Host: host, ExpiresAt: now.Add(CodeTTL).Unix(),
	})
	return code, err
}

// TakeCode consumes a code issued for host and returns its user id. A code
// works once, for its host, until it expires.
func (s *Service) TakeCode(ctx context.Context, code, host string) (string, error) {
	if code == "" {
		return "", ErrBadCode
	}
	row, err := s.Store.TakePreviewCode(ctx, db.TakePreviewCodeParams{CodeHash: hash(code), ExpiresAt: s.now().Unix()})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Host != host {
		return "", ErrBadCode
	}
	if err != nil {
		return "", err
	}
	return row.UserID, nil
}

// CreateSession starts a preview session of userID on host.
func (s *Service) CreateSession(ctx context.Context, userID, host string) (string, error) {
	now := s.now()
	_ = s.Store.DeleteExpiredPreviewSessions(ctx, now.Unix())
	token := id.Random(32)
	err := s.Store.CreatePreviewSession(ctx, db.CreatePreviewSessionParams{
		TokenHash: hash(token), UserID: userID, Host: host, CreatedAt: now.Unix(), ExpiresAt: now.Add(SessionTTL).Unix(),
	})
	return token, err
}

// SessionUser returns the user of a preview session, which only works on
// the host it was created for.
func (s *Service) SessionUser(ctx context.Context, token, host string) (*db.User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	row, err := s.Store.GetPreviewSessionUser(ctx, db.GetPreviewSessionUserParams{TokenHash: hash(token), Host: host, ExpiresAt: s.now().Unix()})
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
	return s.Store.DeletePreviewSession(ctx, hash(token))
}

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
