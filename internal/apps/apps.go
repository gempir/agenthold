package apps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Paths in an app machine, relative to its home.
const (
	AppDir      = "app"
	ReleasesDir = AppDir + "/releases"
	CurrentLink = AppDir + "/current"
	// KeepReleases is how many releases a deploy keeps (the current one among them).
	KeepReleases = 3
)

// DefaultExclude is left out of a deploy unless the caller says otherwise.
var DefaultExclude = []string{".git"}

// CreateParams describes a new app.
type CreateParams struct {
	OwnerID          string
	Name             string
	CreatedByAgentID string
	CPUs             float64
	MemoryMB         int64
	// IdleMinutes 0 means the app never sleeps.
	IdleMinutes int64
}

// Create inserts an app machine and starts provisioning it.
func (m *Manager) Create(ctx context.Context, p CreateParams) (db.Agent, error) {
	now := m.now().Unix()
	appID := id.New("ap")
	var provision func()
	err := m.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateApp(ctx, db.CreateAppParams{
			ID: appID, OwnerID: p.OwnerID, Name: p.Name, Image: "base", CreatedByAgentID: p.CreatedByAgentID,
			Cpus: p.CPUs, MemoryMb: p.MemoryMB, IdleMinutes: p.IdleMinutes, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		var err error
		provision, err = m.Sandboxes.Provision(ctx, q, appID)
		return err
	})
	if err != nil {
		return db.Agent{}, err
	}
	provision()
	return m.Store.GetAgent(ctx, appID)
}

// DeployParams describes a deploy.
type DeployParams struct {
	App db.Agent
	// SourceID is the machine the files come from; SourceDir a directory in it
	// (relative to its home or absolute).
	SourceID  string
	SourceDir string
	// Command runs in ~/app/current with PORT=<Port> and Env.
	Command string
	Port    int
	Env     map[string]string
	// Exclude defaults to DefaultExclude.
	Exclude []string
	// Expose creates (or updates) the exposure of Port with Access.
	Expose bool
	Access preview.Access
	// ExposedBy is the exposure's created_by for a new one.
	ExposedBy string
	// ReadyTimeout bounds the wait for the port (default 60 s).
	ReadyTimeout time.Duration
	// Actor is who deploys, for the audit log: a user id, and the agent if
	// an agent deploys.
	ActorUserID  string
	ActorAgentID string
}

// DeployResult reports a deploy.
type DeployResult struct {
	Release  int
	Exposure *db.Exposure
	URL      string
	// Ready: the port accepted a connection before the timeout. When it did
	// not, the previous release (if any) serves again (RolledBack) and Logs
	// holds the service's last log lines.
	Ready      bool
	RolledBack bool
	Logs       string
	Service    *tunnel.ServiceState
}

// ErrDeployBusy is returned while another deploy of the same app runs.
var ErrDeployBusy = errors.New("another deploy of this app is in progress")

func (m *Manager) lock(appID string) (func(), bool) {
	v, _ := m.locks.LoadOrStore(appID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// Deploy copies the source directory into a new release of the app, points
// ~/app/current at it, (re)starts the app's service and waits for its port.
// A copy that fails leaves the previous release serving; so does a release
// whose port does not come up (it is rolled back).
func (m *Manager) Deploy(ctx context.Context, p DeployParams) (DeployResult, error) {
	var res DeployResult
	if p.App.Kind != KindApp {
		return res, invalid("not an app")
	}
	if p.Port < 1 || p.Port > 65535 {
		return res, invalid("port must be between 1 and 65535")
	}
	if strings.TrimSpace(p.SourceDir) == "" {
		return res, invalid("source directory is required")
	}
	env := maps.Clone(p.Env)
	if env == nil {
		env = map[string]string{}
	}
	env["PORT"] = strconv.Itoa(p.Port)
	spec := Spec{Name: AppService, Command: p.Command, Workdir: CurrentLink, Env: env, Enabled: true}
	if err := spec.Validate(); err != nil {
		return res, err
	}
	unlock, ok := m.lock(p.App.ID)
	if !ok {
		return res, ErrDeployBusy
	}
	defer unlock()
	if m.Wake == nil {
		return res, errors.New("deploy is not available")
	}
	src, err := m.Wake(ctx, p.SourceID, func(string) {})
	if err != nil {
		return res, fmt.Errorf("source machine: %w", err)
	}
	app, err := m.Wake(ctx, p.App.ID, func(string) {})
	if err != nil {
		return res, fmt.Errorf("app machine: %w", err)
	}

	releases, current, err := m.releases(ctx, app)
	if err != nil {
		return res, err
	}
	res.Release = 1
	if len(releases) > 0 {
		res.Release = releases[len(releases)-1] + 1
	}
	dir := ReleasesDir + "/" + strconv.Itoa(res.Release)
	exclude := p.Exclude
	if exclude == nil {
		exclude = DefaultExclude
	}
	if err := m.copy(ctx, src, p.SourceDir, exclude, app, dir); err != nil {
		_ = app.Remove(context.WithoutCancel(ctx), dir, true)
		return res, err
	}

	prevSpec, prevErr := m.Get(ctx, p.App.ID, AppService)
	if err := app.Symlink(ctx, CurrentLink, "releases/"+strconv.Itoa(res.Release)); err != nil {
		return res, err
	}
	changed, _, err := m.Put(ctx, p.App.ID, spec)
	if err != nil {
		return res, err
	}
	if !changed {
		// Same command: only the files changed, which the service does not notice.
		if _, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceRestart, Name: AppService}); err != nil {
			return res, err
		}
	}

	if p.Expose && m.Preview.Enabled() {
		e, err := m.ensureExposure(ctx, p)
		if err != nil {
			return res, err
		}
		res.Exposure, res.URL = &e, m.Preview.URL(e)
	}

	timeout := p.ReadyTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	res.Ready = m.waitPort(ctx, app, p.Port, timeout)
	if !res.Ready {
		var logs bytes.Buffer
		_ = app.ServiceLogs(ctx, AppService, 40, false, &logs)
		res.Logs = logs.String()
		if current > 0 {
			res.RolledBack = m.rollback(ctx, app, p.App.ID, current, prevSpec, prevErr == nil)
		}
	}
	if sts, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceStatus}); err == nil {
		for _, st := range sts {
			if st.Name == AppService {
				res.Service = &st
			}
		}
	}
	m.prune(ctx, app)
	m.Audit.Record(ctx, audit.Entry{
		AgentID: p.App.ID, UserID: p.ActorUserID, Action: audit.AppDeploy, Target: strconv.Itoa(res.Release),
		Details: map[string]any{
			"source": p.SourceID, "sourceDir": p.SourceDir, "port": p.Port, "command": p.Command,
			"ready": res.Ready, "rolledBack": res.RolledBack, "fromAgentId": p.ActorAgentID,
		},
	})
	return res, nil
}

// releases lists the numbered releases (ascending) and the one current points at (0 if none).
func (m *Manager) releases(ctx context.Context, app *tunnel.Session) ([]int, int, error) {
	var out []int
	ls, err := app.List(ctx, ReleasesDir)
	if err != nil && !isNotExist(err) {
		return nil, 0, err
	}
	for _, e := range ls.Entries {
		if n, err := strconv.Atoi(e.Name); err == nil && n > 0 && e.IsDir {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	current := 0
	if ls, err := app.List(ctx, AppDir); err == nil {
		for _, e := range ls.Entries {
			if e.Name == path.Base(CurrentLink) && e.IsLink {
				current, _ = strconv.Atoi(path.Base(e.Target))
			}
		}
	}
	return out, current, nil
}

func isNotExist(err error) bool {
	var re *tunnel.RemoteError
	return errors.As(err, &re) && strings.Contains(re.Msg, "no such file or directory")
}

// copy streams the source directory, as a tar archive through the
// controller, into dir of the app.
func (m *Manager) copy(ctx context.Context, src *tunnel.Session, srcDir string, exclude []string, app *tunnel.Session, dir string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limit := m.Limits.DeployMaxBytes
	pr, pw := io.Pipe()
	tarErr := make(chan error, 1)
	go func() {
		err := src.Tar(ctx, srcDir, exclude, limit, pw)
		_ = pw.CloseWithError(err)
		tarErr <- err
	}()
	err := app.Untar(ctx, dir, limit, pr)
	_ = pr.CloseWithError(errors.New("copy ended"))
	cancel()
	if terr := <-tarErr; terr != nil && !errors.Is(terr, context.Canceled) && !errors.Is(terr, io.ErrClosedPipe) {
		return fmt.Errorf("read %s: %w", srcDir, terr)
	}
	if err != nil {
		return fmt.Errorf("copy into the app: %w", err)
	}
	return nil
}

// ensureExposure makes the app's port reachable: the exposure of that port
// (access updated), or a new one; an exposure deploy made for another port
// moves to this one under the same name.
func (m *Manager) ensureExposure(ctx context.Context, p DeployParams) (db.Exposure, error) {
	access := p.Access
	if access == "" {
		access = preview.Private
	}
	if e, err := m.Preview.ByPort(ctx, p.App.ID, p.Port); err == nil {
		if preview.Access(e.Access) == access {
			return e, nil
		}
		return m.Preview.Update(ctx, e, p.App.OwnerID, preview.Patch{Access: access})
	} else if !errors.Is(err, preview.ErrNotFound) {
		return db.Exposure{}, err
	}
	name := ""
	rows, err := m.Preview.List(ctx, p.App.ID)
	if err != nil {
		return db.Exposure{}, err
	}
	for _, e := range rows {
		if strings.HasPrefix(e.CreatedBy, "deploy:") {
			if err := m.Preview.Delete(ctx, e, p.App.OwnerID); err != nil {
				return db.Exposure{}, err
			}
			name = e.Name
			break
		}
	}
	return m.Preview.Create(ctx, preview.CreateParams{
		Agent: p.App, Port: p.Port, Name: name, Access: access, CreatedBy: "deploy:" + p.ExposedBy,
	})
}

// waitPort waits until something accepts connections on the app's port. It
// gives up early when the service keeps exiting (it crashed twice since the
// wait began).
func (m *Manager) waitPort(ctx context.Context, app *tunnel.Session, port int, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := -1
	for {
		c, err := app.Dial(ctx, port)
		if err == nil {
			_ = c.Close()
			return true
		}
		if sts, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceStatus}); err == nil {
			for _, st := range sts {
				if st.Name != AppService {
					continue
				}
				if base < 0 {
					base = st.Restarts
				}
				if st.Restarts-base >= 2 && st.State != tunnel.ServiceRunning {
					return false
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// rollback points current back at release prev and restores the previous
// service definition.
func (m *Manager) rollback(ctx context.Context, app *tunnel.Session, appID string, prev int, spec Spec, hadSpec bool) bool {
	ctx = context.WithoutCancel(ctx)
	if err := app.Symlink(ctx, CurrentLink, "releases/"+strconv.Itoa(prev)); err != nil {
		m.log().Warn("deploy rollback", "app", appID, "err", err)
		return false
	}
	if hadSpec {
		if changed, _, err := m.Put(ctx, appID, spec); err != nil {
			m.log().Warn("deploy rollback", "app", appID, "err", err)
			return false
		} else if changed {
			return true // the changed definition restarted it
		}
	}
	if _, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceRestart, Name: AppService}); err != nil {
		m.log().Warn("deploy rollback", "app", appID, "err", err)
		return false
	}
	return true
}

// prune removes all but the newest KeepReleases releases, never the current one.
func (m *Manager) prune(ctx context.Context, app *tunnel.Session) {
	releases, current, err := m.releases(ctx, app)
	if err != nil || len(releases) <= KeepReleases {
		return
	}
	for _, n := range releases[:len(releases)-KeepReleases] {
		if n == current {
			continue
		}
		_ = app.Remove(ctx, ReleasesDir+"/"+strconv.Itoa(n), true)
	}
}

// Exec runs a shell command in the app (in ~/app/current when it exists) as
// the agent account.
func (m *Manager) Exec(ctx context.Context, appID, command string, timeout int) (tunnel.ExecReply, error) {
	if m.Wake == nil {
		return tunnel.ExecReply{}, errors.New("exec is not available")
	}
	s, err := m.Wake(ctx, appID, func(string) {})
	if err != nil {
		return tunnel.ExecReply{}, err
	}
	script := `cd "$HOME/` + CurrentLink + `" 2>/dev/null || true` + "\n" + command
	return s.Exec(ctx, []string{"/bin/sh", "-lc", script}, "", nil, timeout, 64<<10)
}
