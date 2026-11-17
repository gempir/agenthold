package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// ServicesFile is where the host delivers the desired services of a machine
// (a JSON array of tunnel.ServiceSpec) in the boot configuration's files.
// sandboxd starts them at boot without waiting for the server, and rewrites
// the file when the server changes them at runtime, so a restarted daemon
// comes back with the current set.
const ServicesFile = "/etc/agenthold/services.json"

// ServiceLogDir is where service logs live, relative to the agent's home:
// <dir>/<name>/log and the rotated <dir>/<name>/log.1.
const ServiceLogDir = ".agenthold/services"

// Supervision timing.
var (
	serviceMinBackoff = time.Second
	serviceMaxBackoff = 30 * time.Second
	// serviceHealthy is how long a run must last to reset the backoff.
	serviceHealthy = 60 * time.Second
	// serviceStopGrace is how long a service has after SIGTERM.
	serviceStopGrace = 5 * time.Second
)

var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidServiceName reports whether name can name a service: lowercase
// letters, digits and hyphens, up to 40, not starting with a hyphen.
func ValidServiceName(name string) bool { return serviceNameRE.MatchString(name) }

// ParseServices decodes the services file.
func ParseServices(b []byte) ([]tunnel.ServiceSpec, error) {
	var specs []tunnel.ServiceSpec
	if err := json.Unmarshal(b, &specs); err != nil {
		return nil, fmt.Errorf("services: %w", err)
	}
	return specs, validateSpecs(specs)
}

func validateSpecs(specs []tunnel.ServiceSpec) error {
	seen := map[string]bool{}
	for _, s := range specs {
		if !ValidServiceName(s.Name) {
			return fmt.Errorf("invalid service name %q", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("service %q is listed twice", s.Name)
		}
		seen[s.Name] = true
		if s.Command == "" {
			return fmt.Errorf("service %q has no command", s.Name)
		}
	}
	return nil
}

// supervisor runs the machine's services. Each one runs as the agent account
// in its own cgroup (where cgroup v2 is available; its process group
// otherwise), independent of harnesses and terminals, and is restarted with
// exponential backoff when it exits.
type supervisor struct {
	d   *Daemon
	log *slog.Logger
	// persist is the file the desired set is written to ("" for none).
	persist string

	// Lock order: ops, then mu, then a service's mu. Nothing that holds a
	// service's mu takes mu or ops, and nothing that holds mu takes ops.
	// ops serializes changes (apply, start, stop, restart), so their effects
	// on the processes never interleave; mu guards the map and ctx; a
	// service's mu guards its spec and state.
	ops  sync.Mutex
	mu   sync.Mutex
	ctx  context.Context
	svcs map[string]*service
}

type service struct {
	sup  *supervisor
	name string
	out  *logSink

	mu        sync.Mutex
	spec      tunnel.ServiceSpec
	state     string
	pid       int
	restarts  int
	exitCode  *int
	signal    string
	errMsg    string
	startedAt time.Time

	// The supervision loop, while the service is enabled.
	stop chan struct{}
	done chan struct{}
}

func newSupervisor(d *Daemon, persist string) *supervisor {
	return &supervisor{d: d, log: d.log, persist: persist, svcs: map[string]*service{}}
}

// start begins supervising specs. Services launched later need ctx alive;
// the daemon calls stopAll when it ends.
func (s *supervisor) start(ctx context.Context, specs []tunnel.ServiceSpec) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	if len(specs) > 0 {
		if err := s.apply(specs, false); err != nil {
			s.log.Warn("boot services", "err", err)
		}
	}
}

func (s *supervisor) stopAll() {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	svcs := make([]*service, 0, len(s.svcs))
	for _, sv := range s.svcs {
		svcs = append(svcs, sv)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, sv := range svcs {
		wg.Go(sv.halt)
	}
	wg.Wait()
}

// apply makes specs the desired set: new services start, removed ones stop,
// changed ones restart, and unchanged ones are left alone.
func (s *supervisor) apply(specs []tunnel.ServiceSpec, persist bool) error {
	if err := validateSpecs(specs); err != nil {
		return err
	}
	s.mu.Lock()
	want := map[string]tunnel.ServiceSpec{}
	for _, sp := range specs {
		want[sp.Name] = sp
	}
	var removed []*service
	for name, sv := range s.svcs {
		if _, ok := want[name]; !ok {
			removed = append(removed, sv)
			delete(s.svcs, name)
		}
	}
	type change struct {
		sv      *service
		spec    tunnel.ServiceSpec
		changed bool
	}
	var changes []change
	for _, sp := range specs {
		sv, ok := s.svcs[sp.Name]
		if !ok {
			sv = s.newService(sp)
			s.svcs[sp.Name] = sv
			changes = append(changes, change{sv, sp, true})
			continue
		}
		changes = append(changes, change{sv, sp, !sameSpec(sv.current(), sp)})
	}
	s.mu.Unlock()

	for _, sv := range removed {
		sv.halt()
		sv.out.close()
		s.log.Info("service removed", "service", sv.name)
	}
	for _, c := range changes {
		c.sv.setSpec(c.spec)
		switch {
		case !c.spec.Enabled:
			c.sv.halt()
		case c.changed && c.sv.looping():
			c.sv.restartNow()
		default:
			c.sv.launch()
		}
	}
	if persist {
		s.save()
	}
	return nil
}

func sameSpec(a, b tunnel.ServiceSpec) bool {
	if a.Command != b.Command || a.Workdir != b.Workdir || len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if bv, ok := b.Env[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func (s *supervisor) newService(sp tunnel.ServiceSpec) *service {
	logPath := ""
	dir := filepath.Join(s.d.acct.home, ServiceLogDir, sp.Name)
	if err := s.d.mkdirAll(dir); err == nil {
		logPath = filepath.Join(dir, "log")
	} else {
		s.log.Warn("service log directory", "service", sp.Name, "err", err)
	}
	return &service{sup: s, name: sp.Name, spec: sp, state: tunnel.ServiceStopped, out: newLogSink(logPath, s.d.own)}
}

// save writes the desired set where the next daemon start finds it.
func (s *supervisor) save() {
	if s.persist == "" {
		return
	}
	s.mu.Lock()
	specs := make([]tunnel.ServiceSpec, 0, len(s.svcs))
	for _, sv := range s.svcs {
		specs = append(specs, sv.current())
	}
	s.mu.Unlock()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	b, _ := json.Marshal(specs)
	if err := writeFileAtomicMode(s.persist, b, 0o644); err != nil {
		s.log.Warn("save services", "err", err)
	}
}

func (s *supervisor) get(name string) (*service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.svcs[name]
	if !ok {
		return nil, fmt.Errorf("no service %q", name)
	}
	return sv, nil
}

func (s *supervisor) status() []tunnel.ServiceState {
	s.mu.Lock()
	svcs := make([]*service, 0, len(s.svcs))
	for _, sv := range s.svcs {
		svcs = append(svcs, sv)
	}
	s.mu.Unlock()
	out := make([]tunnel.ServiceState, 0, len(svcs))
	for _, sv := range svcs {
		out = append(out, sv.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (sv *service) current() tunnel.ServiceSpec {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.spec
}

func (sv *service) setSpec(sp tunnel.ServiceSpec) {
	sv.mu.Lock()
	sv.spec = sp
	sv.mu.Unlock()
}

func (sv *service) setEnabled(on bool) {
	sv.mu.Lock()
	sv.spec.Enabled = on
	sv.mu.Unlock()
}

func (sv *service) status() tunnel.ServiceState {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	st := tunnel.ServiceState{
		Name: sv.name, State: sv.state, Pid: sv.pid, Restarts: sv.restarts,
		ExitCode: sv.exitCode, Signal: sv.signal, Error: sv.errMsg,
	}
	if !sv.startedAt.IsZero() {
		st.StartedAt = sv.startedAt.Unix()
	}
	return st
}

func (sv *service) looping() bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.stop != nil
}

// launch starts the supervision loop unless it runs. It reads the
// supervisor's context before taking the service's lock (see the lock order).
func (sv *service) launch() {
	sv.sup.mu.Lock()
	ctx := sv.sup.ctx
	sv.sup.mu.Unlock()
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.stop != nil {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		return
	}
	sv.stop, sv.done = make(chan struct{}), make(chan struct{})
	go sv.loop(sv.stop, sv.done)
}

// halt stops the loop and the service, and waits for both.
func (sv *service) halt() {
	sv.mu.Lock()
	stop, done := sv.stop, sv.done
	sv.stop, sv.done = nil, nil
	sv.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// restartNow stops the service (if it runs) and starts it again with a
// fresh backoff. It returns once the old process is gone.
func (sv *service) restartNow() {
	if sv.looping() {
		sv.note("restarting")
		sv.halt()
		sv.set(func() { sv.restarts++ })
	}
	sv.launch()
}

func (sv *service) set(f func()) {
	sv.mu.Lock()
	f()
	sv.mu.Unlock()
}

func (sv *service) note(format string, args ...any) {
	_, _ = fmt.Fprintf(sv.out, "agenthold: "+format+"\n", args...)
}

// loop runs the service until stop is closed.
func (sv *service) loop(stop, done chan struct{}) {
	defer close(done)
	backoff := serviceMinBackoff
	for {
		began := time.Now()
		how, err := sv.runOnce(stop)
		if how == runStopped {
			sv.set(func() { sv.state, sv.pid = tunnel.ServiceStopped, 0 })
			sv.note("stopped")
			return
		}
		if time.Since(began) >= serviceHealthy {
			backoff = serviceMinBackoff
		}
		state := tunnel.ServiceBackoff
		if how == runFailed {
			state = tunnel.ServiceFailed
			sv.note("could not start: %v; retrying in %s", err, backoff)
		} else {
			sv.note("exited (%s); restarting in %s", sv.exitText(), backoff)
		}
		sv.set(func() { sv.state, sv.pid = state, 0 })
		t := time.NewTimer(backoff)
		select {
		case <-stop:
			t.Stop()
			sv.set(func() { sv.state = tunnel.ServiceStopped })
			sv.note("stopped")
			return
		case <-t.C:
			backoff = min(backoff*2, serviceMaxBackoff)
		}
		sv.set(func() { sv.restarts++ })
	}
}

func (sv *service) exitText() string {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.signal != "" {
		return "signal " + sv.signal
	}
	if sv.exitCode != nil {
		return fmt.Sprintf("code %d", *sv.exitCode)
	}
	return "unknown status"
}

type runEnd int

const (
	runExited runEnd = iota
	runFailed
	runStopped
)

// runOnce starts the service and waits until it exits or stop is closed.
// Whatever ends it, everything it started is killed.
func (sv *service) runOnce(stop chan struct{}) (runEnd, error) {
	spec := sv.current()
	d := sv.sup.d
	dir := d.resolve(spec.Workdir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		err := fmt.Errorf("working directory %s does not exist", dir)
		sv.set(func() { sv.errMsg = err.Error() })
		return runFailed, err
	}
	cg := newServiceCgroup(sv.sup.log, sv.name)
	outR, outW, err := os.Pipe()
	if err != nil {
		cg.remove()
		return runFailed, err
	}
	cmd, err := d.startService(spec, dir, outW, cg)
	_ = outW.Close()
	if err != nil {
		_ = outR.Close()
		cg.remove()
		sv.set(func() { sv.errMsg = err.Error() })
		return runFailed, err
	}
	pid := cmd.Process.Pid
	sv.set(func() {
		sv.state, sv.pid, sv.startedAt, sv.errMsg = tunnel.ServiceRunning, pid, time.Now(), ""
	})
	sv.note("started (pid %d): %s", pid, spec.Command)
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(sv.out, outR); close(copied) }()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	how := runExited
	select {
	case err = <-waited:
	case <-stop:
		how = runStopped
		err = terminate(pid, cg, waited)
	}
	// Helpers it left behind go too, so a restart finds its port free.
	cg.remove()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-copied:
	case <-time.After(time.Second):
	}
	_ = outR.Close()
	<-copied
	code, sig := exitStatus(err)
	sv.set(func() { sv.exitCode, sv.signal = code, sig })
	return how, err
}

// terminate asks the service to end, then kills it and its cgroup.
func terminate(pid int, cg *harnessCgroup, waited chan error) error {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case err := <-waited:
		return err
	case <-time.After(serviceStopGrace):
	}
	cg.kill()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return <-waited
}

func exitStatus(err error) (*int, string) {
	var ee *exec.ExitError
	if err == nil {
		zero := 0
		return &zero, ""
	}
	if !errors.As(err, &ee) {
		return nil, ""
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return nil, ws.Signal().String()
	}
	code := ee.ExitCode()
	return &code, ""
}

// startService starts the service's command with the login shell, in cg
// when the kernel allows it.
func (d *Daemon) startService(spec tunnel.ServiceSpec, dir string, out *os.File, cg *harnessCgroup) (*exec.Cmd, error) {
	shell := d.shell()[0]
	start := func(inCgroup bool) (*exec.Cmd, error) {
		cmd := exec.Command(shell, "-lc", spec.Command) //nolint:noctx // the supervisor ends it
		cmd.Dir = dir
		cmd.Env = d.env(spec.Env, shell)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if d.acct.switchTo {
			cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
		}
		if inCgroup {
			cg.attach(cmd.SysProcAttr)
		}
		cmd.Stdout, cmd.Stderr = out, out
		return cmd, cmd.Start()
	}
	if cg.path() != "" {
		cmd, err := start(true)
		if err == nil {
			return cmd, nil
		}
		d.log.Warn("starting the service in its cgroup failed, using its process group only", "err", err)
		cg.remove()
	}
	return start(false)
}

// service serves OpService.
func (d *Daemon) service(ctx context.Context, st net.Conn, o tunnel.Open) error {
	req := o.Service
	if req == nil {
		return errors.New("service: no request")
	}
	s := d.services
	switch req.Action {
	case tunnel.ServiceApply:
		s.ops.Lock()
		err := s.apply(req.Specs, true)
		s.ops.Unlock()
		if err != nil {
			return err
		}
	case tunnel.ServiceStart, tunnel.ServiceStop, tunnel.ServiceRestart:
		s.ops.Lock()
		defer s.ops.Unlock()
		sv, err := s.get(req.Name)
		if err != nil {
			return err
		}
		switch req.Action {
		case tunnel.ServiceStart:
			sv.setEnabled(true)
			sv.launch()
		case tunnel.ServiceStop:
			sv.setEnabled(false)
			sv.halt()
		case tunnel.ServiceRestart:
			sv.setEnabled(true)
			sv.restartNow()
		}
		s.save()
	case tunnel.ServiceStatus:
	case tunnel.ServiceLogs:
		return d.serviceLogs(ctx, st, req)
	default:
		return fmt.Errorf("unknown service action %q", req.Action)
	}
	return tunnel.WriteJSON(st, tunnel.ServicesReply{Services: s.status()})
}

func (d *Daemon) serviceLogs(ctx context.Context, st net.Conn, req *tunnel.ServiceRequest) error {
	sv, err := d.services.get(req.Name)
	if err != nil {
		return err
	}
	lines := req.Lines
	if lines <= 0 {
		lines = 100
	}
	lines = min(lines, 10000)
	var ch chan []byte
	if req.Follow {
		var unsub func()
		ch, unsub = sv.out.subscribe()
		defer unsub()
	}
	if err := tunnel.WriteJSON(st, tunnel.OK{OK: true}); err != nil {
		return err
	}
	w := tunnel.DataWriter(st)
	if b := sv.out.lastLines(lines); len(b) > 0 {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	if !req.Follow {
		return tunnel.WriteFrame(st, tunnel.FrameEOF, nil)
	}
	// The server ends a follow by closing the stream.
	gone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, st); close(gone) }()
	for {
		select {
		case b := <-ch:
			if _, err := w.Write(b); err != nil {
				return nil
			}
		case <-gone:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// loadServices reads the services file (missing means none).
func loadServices(path string) ([]tunnel.ServiceSpec, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseServices(b)
}

func writeFileAtomicMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agenthold-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
