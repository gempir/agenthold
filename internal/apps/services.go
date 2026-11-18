// Package apps is the controller side of services and apps.
//
// A service is a long-running command sandboxd supervises in a machine (any
// kind). Its desired state lives in the services table; it reaches the guest
// in the boot files of every start (so services come up without the
// controller) and through the tunnel's service operation while the machine
// runs. An app is a machine of kind "app": no harness, no sessions, it runs
// services and is reached through exposures. Deploy copies a directory from
// one machine into an app and (re)starts its service.
package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Machine kinds (agents.kind).
const (
	KindAgent = "agent"
	KindApp   = "app"
)

// AppService is the name of the service deploy defines in an app.
const AppService = "app"

// MaxServices bounds the services of one machine.
const MaxServices = 20

// Errors.
var (
	ErrNotFound   = errors.New("no such service")
	ErrTooMany    = fmt.Errorf("a machine can have at most %d services", MaxServices)
	ErrNotRunning = errors.New("the machine is not running; start it first")
)

// InvalidError is a validation failure (an API 400).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// ApplyError is a change that was stored but could not be applied to the
// running machine (for example an old sandboxd). The machine gets it at its
// next start.
type ApplyError struct{ Err error }

func (e *ApplyError) Error() string {
	return "saved, but the running machine did not take it: " + e.Err.Error()
}
func (e *ApplyError) Unwrap() error { return e.Err }

func (m *Manager) pushStored(ctx context.Context, agentID string) ([]tunnel.ServiceState, error) {
	st, err := m.Push(ctx, agentID)
	if err != nil {
		return nil, &ApplyError{Err: err}
	}
	return st, nil
}

// Machines is what the manager needs of the node layer (node.Node has it):
// whether a machine's sandboxd is connected, and streams to it.
type Machines interface {
	Connected(ctx context.Context, agentID string) (bool, error)
	OpenStream(ctx context.Context, agentID string, open tunnel.Open) (io.ReadWriteCloser, error)
}

// Manager implements services and apps.
type Manager struct {
	Store     *store.Store
	Node      Machines
	Sandboxes *sandbox.Manager
	Preview   *preview.Service
	Audit     *audit.Log
	Limits    config.Apps
	// Wake returns a connected session for a machine, starting it if it
	// sleeps (the API's Wake). Set before deploying.
	Wake func(ctx context.Context, agentID string, status func(string)) (*tunnel.Session, error)
	Log  *slog.Logger
	Now  func() time.Time

	locks  sync.Map // app id -> *sync.Mutex: one deploy at a time
	owners sync.Map // owner id -> *sync.Mutex: app creation
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Spec is a service as the API and tools describe it.
type Spec struct {
	Name    string
	Command string
	Workdir string
	Env     map[string]string
	Enabled bool
}

// Validate checks a spec.
func (s Spec) Validate() error {
	if !sandboxd.ValidServiceName(s.Name) {
		return invalid("service name must be 1 to 40 lowercase letters, digits and hyphens, not starting with a hyphen")
	}
	if strings.TrimSpace(s.Command) == "" || len(s.Command) > 8192 || strings.ContainsRune(s.Command, 0) {
		return invalid("command must be 1 to 8192 bytes")
	}
	if len(s.Workdir) > 4096 || strings.ContainsRune(s.Workdir, 0) {
		return invalid("invalid workdir")
	}
	if len(s.Env) > 100 {
		return invalid("at most 100 environment variables")
	}
	for k, v := range s.Env {
		if !envNameRE.MatchString(k) || len(k) > 256 {
			return invalid("invalid environment variable name %q", k)
		}
		if len(v) > 32<<10 || strings.ContainsRune(v, 0) {
			return invalid("environment variable %s is too long or contains NUL", k)
		}
	}
	return nil
}

// SpecOf converts a row.
func SpecOf(r db.Service) Spec {
	env := map[string]string{}
	_ = json.Unmarshal([]byte(r.Env), &env)
	return Spec{Name: r.Name, Command: r.Command, Workdir: r.Workdir, Env: env, Enabled: r.Enabled != 0}
}

func (s Spec) tunnel() tunnel.ServiceSpec {
	return tunnel.ServiceSpec{Name: s.Name, Command: s.Command, Workdir: s.Workdir, Env: s.Env, Enabled: s.Enabled}
}

// Specs returns a machine's services in their guest form.
func (m *Manager) Specs(ctx context.Context, agentID string) ([]tunnel.ServiceSpec, error) {
	rows, err := m.Store.ListServices(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]tunnel.ServiceSpec, 0, len(rows))
	for _, r := range rows {
		out = append(out, SpecOf(r).tunnel())
	}
	return out, nil
}

// BootFiles delivers the machine's services to sandboxd at every start. The
// file is written even when there are none, so removed services stay gone.
func (m *Manager) BootFiles(ctx context.Context, agentID string) (map[string][]byte, error) {
	specs, err := m.Specs(ctx, agentID)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(specs)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{sandboxd.ServicesFile: b}, nil
}

// connected returns the machine's session if its sandboxd is connected.
func (m *Manager) connected(ctx context.Context, agentID string) (*tunnel.Session, bool, error) {
	ok, err := m.Node.Connected(ctx, agentID)
	if err != nil || !ok {
		return nil, false, err
	}
	return tunnel.NewClient(agentID, func(ctx context.Context, o tunnel.Open) (io.ReadWriteCloser, error) {
		return m.Node.OpenStream(ctx, agentID, o)
	}), true, nil
}

// Push sends the desired services to a running machine and returns their
// status. A machine that is not connected gets them at its next start
// (nil, nil).
func (m *Manager) Push(ctx context.Context, agentID string) ([]tunnel.ServiceState, error) {
	s, ok, err := m.connected(ctx, agentID)
	if err != nil || !ok {
		return nil, err
	}
	specs, err := m.Specs(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return s.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: specs})
}

// Put creates or replaces a service and applies it. It reports whether the
// stored definition changed.
func (m *Manager) Put(ctx context.Context, agentID string, s Spec) (bool, []tunnel.ServiceState, error) {
	if err := s.Validate(); err != nil {
		return false, nil, err
	}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	prev, err := m.Store.GetService(ctx, db.GetServiceParams{AgentID: agentID, Name: s.Name})
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if !exists {
		rows, err := m.Store.ListServices(ctx, agentID)
		if err != nil {
			return false, nil, err
		}
		if len(rows) >= MaxServices {
			return false, nil, ErrTooMany
		}
	}
	env, _ := json.Marshal(s.Env)
	changed := !exists || !sameSpec(SpecOf(prev), s)
	if err := m.Store.UpsertService(ctx, db.UpsertServiceParams{
		AgentID: agentID, Name: s.Name, Command: s.Command, Workdir: s.Workdir, Env: string(env), Enabled: b2i(s.Enabled), Now: m.now().Unix(),
	}); err != nil {
		return false, nil, err
	}
	st, err := m.pushStored(ctx, agentID)
	return changed, st, err
}

func sameSpec(a, b Spec) bool {
	if a.Command != b.Command || a.Workdir != b.Workdir || a.Enabled != b.Enabled || len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if bv, ok := b.Env[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Get returns a stored service.
func (m *Manager) Get(ctx context.Context, agentID, name string) (Spec, error) {
	r, err := m.Store.GetService(ctx, db.GetServiceParams{AgentID: agentID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return Spec{}, ErrNotFound
	}
	if err != nil {
		return Spec{}, err
	}
	return SpecOf(r), nil
}

// List returns a machine's stored services.
func (m *Manager) List(ctx context.Context, agentID string) ([]db.Service, error) {
	return m.Store.ListServices(ctx, agentID)
}

// Delete removes a service and stops it.
func (m *Manager) Delete(ctx context.Context, agentID, name string) error {
	n, err := m.Store.DeleteService(ctx, db.DeleteServiceParams{AgentID: agentID, Name: name})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, err = m.pushStored(ctx, agentID)
	return err
}

// SetEnabled starts (enables) or stops (disables) a service.
func (m *Manager) SetEnabled(ctx context.Context, agentID, name string, on bool) ([]tunnel.ServiceState, error) {
	n, err := m.Store.SetServiceEnabled(ctx, db.SetServiceEnabledParams{Enabled: b2i(on), UpdatedAt: m.now().Unix(), AgentID: agentID, Name: name})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return m.pushStored(ctx, agentID)
}

// Restart restarts a service in a running machine (enabling it).
func (m *Manager) Restart(ctx context.Context, agentID, name string) ([]tunnel.ServiceState, error) {
	if _, err := m.Get(ctx, agentID, name); err != nil {
		return nil, err
	}
	if _, err := m.Store.SetServiceEnabled(ctx, db.SetServiceEnabledParams{Enabled: 1, UpdatedAt: m.now().Unix(), AgentID: agentID, Name: name}); err != nil {
		return nil, err
	}
	s, ok, err := m.connected(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotRunning
	}
	// The guest may not know it yet (a service defined while it slept and
	// a daemon that restarted since): apply first.
	if _, err := m.Push(ctx, agentID); err != nil {
		return nil, err
	}
	return s.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceRestart, Name: name})
}

// Status reports the services of a running machine by name, and whether it
// is connected (a stopped machine's services are all down).
func (m *Manager) Status(ctx context.Context, agentID string) (map[string]tunnel.ServiceState, bool, error) {
	s, ok, err := m.connected(ctx, agentID)
	if err != nil || !ok {
		return nil, false, err
	}
	sts, err := s.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceStatus})
	if err != nil {
		return nil, true, err
	}
	out := make(map[string]tunnel.ServiceState, len(sts))
	for _, st := range sts {
		out[st.Name] = st
	}
	return out, true, nil
}

// MaxLogBytes bounds what the controller reads of a service log it keeps in
// memory (a hostile guest decides how much it sends).
const MaxLogBytes = 1 << 20

// Logs copies the last lines of a service's log (and with follow, new
// output until ctx ends) to w. Without follow at most MaxLogBytes are read;
// a follow streams to w without keeping anything.
func (m *Manager) Logs(ctx context.Context, agentID, name string, lines int, follow bool, w io.Writer) error {
	if _, err := m.Get(ctx, agentID, name); err != nil {
		return err
	}
	s, ok, err := m.connected(ctx, agentID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotRunning
	}
	limit := int64(MaxLogBytes)
	if follow {
		limit = 0
	}
	return s.ServiceLogs(ctx, name, lines, follow, w, limit)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
