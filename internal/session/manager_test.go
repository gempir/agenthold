package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gempir/agenthold/internal/harness/fake"
	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// These tests drive the whole harness path with the scripted fake agent: the
// manager, the tunnel, an in-process sandboxd running the fakeagent binary,
// and ACP. Every failure must end the turn with a readable error, and the
// next prompt must work with a fresh harness.

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeagent")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeagent")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "github.com/gempir/agenthold/internal/harness/fakeagent").CombinedOutput(); err != nil {
		panic(string(out))
	}
	fake.SetCommand(fakeBin)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	st   *store.Store
	m    *Manager
	hub  *tunnel.Hub
	home string
	sid  string
}

const agentID = "ag_test"

var socketSeq atomic.Int64

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().Unix()
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "u_1", Name: "U", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, OwnerID: "u_1", Name: "a", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 1024,
		IdleMinutes: 15, PermissionPolicy: PolicyAutoApprove, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	hub := tunnel.NewHub(func(_ context.Context, id, token string) error {
		if id != agentID || token != "tok" {
			return errors.New("bad token")
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(hub)
	home := t.TempDir()
	d, err := sandboxd.New(sandboxd.Config{
		Boot:           sandboxd.Boot{AgentID: agentID, Token: "tok", TunnelURL: "ws" + strings.TrimPrefix(srv.URL, "http")},
		Home:           home,
		Version:        "test",
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		PlatformSocket: filepath.Join(os.TempDir(), fmt.Sprintf("ah-s-%d-%d.sock", os.Getpid(), socketSeq.Add(1))),
	})
	if err != nil {
		t.Fatal(err)
	}
	dctx, stopDaemon := context.WithCancel(ctx)
	daemonDone := make(chan struct{})
	go func() { _ = d.Run(dctx); close(daemonDone) }()

	wake := func(ctx context.Context, id string, _ func(string)) (*tunnel.Session, error) {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return hub.Wait(wctx, id)
	}
	m := NewManager(st, wake, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s, err := m.Create(ctx, agentID, "u_1", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, m: m, hub: hub, home: home, sid: s.ID}
	t.Cleanup(func() {
		m.Close()
		stopDaemon()
		<-daemonDone
		srv.Close()
		// No harness process (or helper of one) outlives its use.
		e.waitDead(e.pids()...)
	})
	return e
}

// script writes the fake agent's script (see internal/harness/fakeagent).
func (e *env) script(lines ...string) {
	e.t.Helper()
	dir := filepath.Join(e.home, ".fakeagent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// pids are the fake agent processes started so far.
func (e *env) pids() []int {
	b, _ := os.ReadFile(filepath.Join(e.home, ".fakeagent", "pids"))
	var out []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// waitPrompt waits until the fake agent has received n prompts in total.
func (e *env) waitPrompt(n int) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(e.home, ".fakeagent", "prompts"))
		if strings.Count(string(b), "\n") >= n {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the harness did not receive prompt %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// prompts is how many prompts the fake agent has received.
func (e *env) prompts() int {
	b, _ := os.ReadFile(filepath.Join(e.home, ".fakeagent", "prompts"))
	return strings.Count(string(b), "\n")
}

// startAndWait prompts and waits until the harness has the prompt.
func (e *env) startAndWait(text string) *Subscription {
	e.t.Helper()
	n := e.prompts()
	sub := e.start(text)
	e.waitPrompt(n + 1)
	return sub
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func (e *env) waitDead(pids ...int) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				e.t.Fatalf("process %d is still running", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// result is one turn as the log shows it.
type result struct {
	events  []Event
	stop    string
	errs    []string
	notices []string
	text    string
}

// start prompts and returns a subscription to the turn's events.
func (e *env) start(text string) *Subscription {
	e.t.Helper()
	seq, err := e.st.MaxSessionSeq(context.Background(), e.sid)
	if err != nil {
		e.t.Fatal(err)
	}
	_, sub, err := e.m.Subscribe(context.Background(), e.sid, seq, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(sub.Close)
	if err := e.m.Prompt(context.Background(), e.sid, "u_1", "U", text, nil); err != nil {
		e.t.Fatal(err)
	}
	return sub
}

// wait reads the turn's events until it ends.
func (e *env) wait(sub *Subscription, within time.Duration) result {
	e.t.Helper()
	var r result
	timeout := time.After(within)
	for {
		select {
		case ev := <-sub.C:
			r.events = append(r.events, ev)
			var d struct {
				Message    string `json:"message"`
				StopReason string `json:"stopReason"`
				Kind       string `json:"sessionUpdate"`
				Content    struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			switch ev.Type {
			case EventError:
				r.errs = append(r.errs, d.Message)
			case EventNotice:
				r.notices = append(r.notices, d.Message)
			case EventUpdate:
				if d.Kind == "agent_message_chunk" {
					r.text += d.Content.Text
				}
			case EventTurnEnd:
				r.stop = d.StopReason
				if e.m.Running(e.sid) {
					e.t.Fatal("session still running after turn_end")
				}
				s, err := e.st.GetAgentSession(context.Background(), e.sid)
				if err != nil || s.Status != "idle" {
					e.t.Fatalf("session status %q after turn_end (%v)", s.Status, err)
				}
				return r
			}
		case <-timeout:
			e.t.Fatalf("turn did not end within %s (events %d)", within, len(r.events))
		}
	}
}

func (e *env) turn(text string) result {
	e.t.Helper()
	return e.wait(e.start(text), 30*time.Second)
}

// ok runs a prompt that must succeed.
func (e *env) ok(text string) result {
	e.t.Helper()
	r := e.turn(text)
	if r.stop != "end_turn" || len(r.errs) > 0 {
		e.t.Fatalf("turn %q: stop %q, errors %q", text, r.stop, r.errs)
	}
	return r
}

// failed runs a prompt that must end with an error containing all of want,
// then checks that the next prompt works.
func (e *env) failed(text string, want ...string) result {
	e.t.Helper()
	r := e.turn(text)
	e.expectError(r, want...)
	e.ok("again")
	return r
}

func (e *env) expectError(r result, want ...string) {
	e.t.Helper()
	if r.stop != "error" || len(r.errs) != 1 {
		e.t.Fatalf("stop %q, errors %q: want one error", r.stop, r.errs)
	}
	for _, w := range want {
		if !strings.Contains(r.errs[0], w) {
			e.t.Fatalf("error %q does not contain %q", r.errs[0], w)
		}
	}
}

func setTimeout(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func TestTurn(t *testing.T) {
	e := newEnv(t)
	if r := e.ok("hello"); r.text != "echo: hello" {
		t.Fatalf("text %q", r.text)
	}
	e.ok("second")
	if n := len(e.pids()); n != 1 {
		t.Fatalf("%d harness processes for two turns", n)
	}
}

func TestHarnessMissing(t *testing.T) {
	e := newEnv(t)
	fake.SetCommand(filepath.Join(t.TempDir(), "no-such-agent"))
	r := e.turn("hello")
	fake.SetCommand(fakeBin)
	e.expectError(r, "could not run Scripted test agent", "no-such-agent")
	e.ok("again")
}

func TestHarnessExitsAtStart(t *testing.T) {
	e := newEnv(t)
	e.script("start exit 3")
	e.failed("hello", "exited with code 3", "scripted exit at start")
}

func TestHarnessKilledAtStart(t *testing.T) {
	e := newEnv(t)
	e.script("start kill")
	e.failed("hello", "was killed (signal 9")
}

func TestInitializeHangs(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.script("initialize hang")
	e.failed("hello", "did not answer initialize and showed no activity for 500ms", "send the message again")
	e.waitDead(e.pids()[0])
}

func TestInitializeError(t *testing.T) {
	e := newEnv(t)
	e.script("initialize error")
	e.failed("hello", "refused initialize", "scripted failure at initialize")
}

func TestSessionNewHangs(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.script("session/new hang")
	e.failed("hello", "did not answer session/new and showed no activity for 500ms")
	e.waitDead(e.pids()[0])
}

func TestSessionNewError(t *testing.T) {
	e := newEnv(t)
	e.script("session/new error")
	e.failed("hello", "could not start a session", "scripted failure at session/new")
}

func TestSessionLoadHangsFallsBack(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.ok("remember me")
	e.m.Disconnect(agentID) // like a sleep: the next turn starts a new process
	e.script("session/load hang")
	// A load that does not come back starts a fresh session, and says so.
	r := e.ok("hello")
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "could not restore this conversation") {
		t.Fatalf("notices %q", r.notices)
	}
	e.ok("again")
}

func TestSlowButAliveSetup(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	// Each step takes three times the idle limit, writing stderr all along.
	e.script("initialize slow 1.5", "session/new slow 1.5")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	e.script("session/load slow 1.5")
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("a slow load was not waited for: notices %q", r.notices)
	}
}

func TestQuietSetupTimesOut(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.script("session/new quiet 1.5")
	e.failed("hello", "did not answer session/new and showed no activity for 500ms")
}

func TestSetupCap(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	setTimeout(t, &setupCap, time.Second)
	e := newEnv(t)
	e.script("session/new slow 3")
	e.failed("hello", "was still working on session/new after 1s, the limit")
}

func TestSessionLostAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if err := os.RemoveAll(filepath.Join(e.home, ".fakeagent", "sessions")); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(e.home, ".fakeagent", "sessions"), 0o755)
	r := e.ok("hello")
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "could not restore this conversation") {
		t.Fatalf("notices %q", r.notices)
	}
}

// restores is how the fake agent restored sessions, in order.
func (e *env) restores() []string {
	b, _ := os.ReadFile(filepath.Join(e.home, ".fakeagent", "restores"))
	return strings.Fields(string(b))
}

func TestSessionLoad(t *testing.T) {
	e := newEnv(t)
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/load"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionResumePreferred(t *testing.T) {
	e := newEnv(t)
	e.script("resume")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	// The harness can do both: resume, which does not replay the history.
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/resume"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionResumeFailsFallsBackToLoad(t *testing.T) {
	e := newEnv(t)
	e.script("resume", "session/resume error")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/resume", "session/load"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionResumeHangsFallsBackToLoad(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.script("resume", "session/resume quiet 5")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/resume", "session/load"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionResumeOnlyFails(t *testing.T) {
	e := newEnv(t)
	e.script("resume", "no-load", "session/resume error")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	r := e.ok("hello")
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "could not restore this conversation") {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/resume"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionResumeOnly(t *testing.T) {
	e := newEnv(t)
	e.script("resume", "no-load")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if r := e.ok("hello"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
	if got := e.restores(); !slices.Equal(got, []string{"session/resume"}) {
		t.Fatalf("restores %q", got)
	}
}

func TestSessionWithoutLoad(t *testing.T) {
	e := newEnv(t)
	e.script("no-load")
	e.ok("remember me")
	e.m.Disconnect(agentID)
	if r := e.ok("hello"); len(r.notices) != 1 {
		t.Fatalf("notices %q", r.notices)
	}
}

func TestLoginRequired(t *testing.T) {
	e := newEnv(t)
	e.script("require-login")
	e.expectError(e.turn("hello"), "not signed in")
	if err := os.WriteFile(filepath.Join(e.home, ".fakeagent", "credentials"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.ok("after login") // the same process: login state is the harness's
}

func TestAutoAuthenticate(t *testing.T) {
	e := newEnv(t)
	e.script("session/new auth")
	e.ok("hello")
}

func TestAutoAuthenticateFails(t *testing.T) {
	e := newEnv(t)
	e.script("session/new auth", "authenticate error")
	e.expectError(e.turn("hello"), "not signed in")
}

func TestAuthenticateHangs(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	e := newEnv(t)
	e.script("session/new auth", "authenticate hang")
	e.failed("hello", "did not answer authenticate and showed no activity for 500ms")
}

func TestExitMidTurn(t *testing.T) {
	e := newEnv(t)
	r := e.failed("exit 4", "Scripted test agent exited with code 4", "scripted exit at prompt")
	if !strings.Contains(r.text, "about to exit") {
		t.Fatalf("partial output lost: %q", r.text)
	}
}

func TestExitZeroMidTurn(t *testing.T) {
	e := newEnv(t)
	e.failed("exit 0", "exited unexpectedly")
}

func TestKilledMidTurn(t *testing.T) {
	e := newEnv(t)
	e.failed("kill", "was killed (signal 9; it may have run out of memory)")
}

func TestPromptError(t *testing.T) {
	e := newEnv(t)
	e.failed("error", "failed the prompt", "scripted failure at prompt")
	if n := len(e.pids()); n != 1 {
		t.Fatalf("an error answer restarted the harness (%d processes)", n)
	}
}

func TestUsageLimitError(t *testing.T) {
	e := newEnv(t)
	// The harness's own message, not the JSON-RPC error around it.
	const msg = "You've hit your usage limit. Try again at Nov 22nd, 2026 11:30 AM."
	r := e.failed("limit", "failed the prompt: "+msg)
	if strings.ContainsAny(r.errs[0], "{}") || strings.Contains(r.errs[0], "Internal error") {
		t.Fatalf("error %q is not readable", r.errs[0])
	}
	if !strings.Contains(r.text, msg) {
		t.Fatalf("text %q", r.text)
	}
	if n := len(e.pids()); n != 1 {
		t.Fatalf("an error answer restarted the harness (%d processes)", n)
	}
}

func TestGarbageOutput(t *testing.T) {
	e := newEnv(t)
	if r := e.ok("garbage"); r.text != "echo: garbage" {
		t.Fatalf("text %q", r.text)
	}
}

func TestOversizedLine(t *testing.T) {
	e := newEnv(t)
	e.failed("bigline", "stopped speaking ACP")
}

func TestHugeStderr(t *testing.T) {
	e := newEnv(t)
	e.ok("stderr 3000000")
	// A failure afterwards shows a bounded stderr tail.
	r := e.failed("exit 1", "exited with code 1")
	if len(r.errs[0]) > 3000 || !strings.Contains(r.errs[0], "scripted exit at prompt") {
		t.Fatalf("error of %d bytes: %.300q", len(r.errs[0]), r.errs[0])
	}
}

func TestStdoutClosed(t *testing.T) {
	e := newEnv(t)
	r := e.failed("closestdout", "was stopped by the sandbox", "closed its stdout")
	// sandboxd's own kill is not mistaken for the OOM killer.
	if strings.Contains(r.errs[0], "memory") {
		t.Fatalf("error %q", r.errs[0])
	}
}

func TestHelperKeepsStdoutOpen(t *testing.T) {
	e := newEnv(t)
	sub := e.start("spawn")
	e.expectError(e.wait(sub, 10*time.Second), "exited unexpectedly")
	b, err := os.ReadFile(filepath.Join(e.home, ".fakeagent", "helper.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	e.waitDead(pid)
	e.ok("again")
}

func TestConcurrentPrompts(t *testing.T) {
	e := newEnv(t)
	sub := e.start("slow 5")
	if err := e.m.Prompt(context.Background(), e.sid, "u_1", "U", "two", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second prompt: %v", err)
	}
	if r := e.wait(sub, 10*time.Second); r.stop != "end_turn" {
		t.Fatalf("stop %q", r.stop)
	}
	// Another session of the same agent shares the harness.
	s2, err := e.m.Create(context.Background(), agentID, "u_1", "")
	if err != nil {
		t.Fatal(err)
	}
	first := e.start("slow 3")
	if err := e.m.Prompt(context.Background(), s2.ID, "u_1", "U", "parallel", nil); err != nil {
		t.Fatal(err)
	}
	e.wait(first, 10*time.Second)
	e.m.Wait()
	if n := len(e.pids()); n != 1 {
		t.Fatalf("%d harness processes", n)
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t)
	sub := e.startAndWait("slow 50")
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	if r := e.wait(sub, 5*time.Second); r.stop != "cancelled" || len(r.errs) != 0 || len(r.notices) != 0 {
		t.Fatalf("stop %q errors %q notices %q", r.stop, r.errs, r.notices)
	}
	if err := e.m.Cancel(context.Background(), e.sid); !errors.Is(err, ErrNoTurn) {
		t.Fatalf("cancel without a turn: %v", err)
	}
	e.ok("again")
}

func TestCancelIgnored(t *testing.T) {
	setTimeout(t, &cancelGrace, 300*time.Millisecond)
	e := newEnv(t)
	sub := e.startAndWait("hang")
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	r := e.wait(sub, 5*time.Second)
	if r.stop != "cancelled" || len(r.errs) != 0 || len(r.notices) != 1 || !strings.Contains(r.notices[0], "did not stop within 300ms") {
		t.Fatalf("stop %q errors %q notices %q", r.stop, r.errs, r.notices)
	}
	e.waitDead(e.pids()[0])
	e.ok("again")
}

func TestCancelWhileStarting(t *testing.T) {
	e := newEnv(t)
	e.script("initialize hang")
	sub := e.start("hello")
	deadline := time.Now().Add(5 * time.Second)
	for len(e.pids()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	if r := e.wait(sub, 5*time.Second); r.stop != "cancelled" || len(r.errs) != 0 {
		t.Fatalf("stop %q errors %q", r.stop, r.errs)
	}
	e.waitDead(e.pids()[0])
	e.ok("again")
}

func TestCancelRacesTurnEnd(t *testing.T) {
	e := newEnv(t)
	e.ok("warm up")
	for range 20 {
		sub := e.start("quick")
		_ = e.m.Cancel(context.Background(), e.sid) // may find no turn
		r := e.wait(sub, 5*time.Second)
		if r.stop != "end_turn" && r.stop != "cancelled" {
			t.Fatalf("stop %q errors %q", r.stop, r.errs)
		}
	}
	e.ok("again")
}

func TestTunnelDropsMidTurn(t *testing.T) {
	e := newEnv(t)
	sub := e.startAndWait("slow 50")
	e.hub.Disconnect(agentID)
	e.expectError(e.wait(sub, 10*time.Second), "lost the connection to the agent's sandbox")
	e.waitDead(e.pids()[0])
	e.ok("again") // sandboxd dials again
}

func TestSandboxStopsMidTurn(t *testing.T) {
	e := newEnv(t)
	sub := e.startAndWait("hang")
	e.m.Disconnect(agentID)
	e.expectError(e.wait(sub, 5*time.Second), "sandbox is stopping")
	e.ok("again")
}

func TestServerShutdownMidTurn(t *testing.T) {
	e := newEnv(t)
	sub := e.startAndWait("hang")
	e.m.Close()
	e.expectError(e.wait(sub, 5*time.Second), "server shut down")
	if err := e.m.Prompt(context.Background(), e.sid, "u_1", "U", "late", nil); err == nil {
		t.Fatal("prompt accepted after Close")
	}
}

func TestServerCrashMidTurn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s2, err := e.m.Create(ctx, agentID, "u_1", "")
	if err != nil {
		t.Fatal(err)
	}
	e.ok("finished turn")
	// What crashes leave behind: a prompt without turn_end, with the session
	// still running (crash during the turn) or already idle (crash between
	// the status update and turn_end).
	if err := e.m.log.append(ctx, e.sid, EventPrompt, "u_1", map[string]string{"text": "cut off"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSessionStatus(ctx, db.SetSessionStatusParams{Status: "running", UpdatedAt: 1, ID: e.sid}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.log.append(ctx, s2.ID, EventPrompt, "u_1", map[string]string{"text": "cut off"}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // a second restart adds nothing
		m2 := NewManager(e.st, nil, nil)
		m2.Close()
	}
	for _, sid := range []string{e.sid, s2.ID} {
		seq, _ := e.st.MaxSessionSeq(ctx, sid)
		evs, sub, err := e.m.Subscribe(ctx, sid, seq-3, nil)
		if err != nil {
			t.Fatal(err)
		}
		sub.Close()
		if len(evs) != 3 || evs[0].Type != EventPrompt || evs[1].Type != EventError || !strings.Contains(string(evs[1].Data), "server restarted") || evs[2].Type != EventTurnEnd {
			t.Fatalf("session %s: events %+v", sid, evs)
		}
		if s, _ := e.st.GetAgentSession(ctx, sid); s.Status != "idle" {
			t.Fatalf("status %q", s.Status)
		}
	}
	// The manager's in-memory sequence numbers are stale after another
	// manager wrote to the log: continue with a fresh one, like a restart.
	m3 := NewManager(e.st, e.m.wake, nil)
	t.Cleanup(m3.Close)
	e.m.Close()
	e.m = m3
	e.ok("again")
}

func TestPermissionCancelled(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetPermissionPolicy(context.Background(), db.SetPermissionPolicyParams{PermissionPolicy: PolicyAsk, UpdatedAt: 1, ID: agentID}); err != nil {
		t.Fatal(err)
	}
	sub := e.start("write " + filepath.Join(e.home, "f.txt") + " hi")
	// Wait for the permission request, then cancel the turn.
	deadline := time.After(10 * time.Second)
	for asked := false; !asked; {
		select {
		case ev := <-sub.C:
			asked = ev.Type == EventPermission
		case <-deadline:
			t.Fatal("no permission request")
		}
	}
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	if r := e.wait(sub, 5*time.Second); r.stop != "cancelled" {
		t.Fatalf("stop %q errors %q", r.stop, r.errs)
	}
	if _, err := os.Stat(filepath.Join(e.home, "f.txt")); err == nil {
		t.Fatal("file written after cancel")
	}
}

// hungOpen is a sandbox whose node accepts the stream but never opens it
// (a connected but hung edge node).
func hungOpen(ctx context.Context, _ string, _ func(string)) (*tunnel.Session, error) {
	return tunnel.NewClient(agentID, func(ctx context.Context, _ tunnel.Open) (io.ReadWriteCloser, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), nil
}

func TestHarnessOpenHangs(t *testing.T) {
	setTimeout(t, &openTimeout, 300*time.Millisecond)
	e := newEnv(t)
	wake := e.m.wake
	e.m.SetWaker(hungOpen)
	e.expectError(e.turn("hello"), "the sandbox did not open the harness within 300ms")
	e.m.SetWaker(wake)
	e.ok("again") // the startup lock was released
}

func TestCancelWhileOpenHangs(t *testing.T) {
	e := newEnv(t)
	wake := e.m.wake
	e.m.SetWaker(hungOpen)
	sub := e.start("hello")
	// The turn is cancellable however far it got.
	deadline := time.Now().Add(5 * time.Second)
	for e.m.Cancel(context.Background(), e.sid) != nil {
		if time.Now().After(deadline) {
			t.Fatal("no turn to cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r := e.wait(sub, 5*time.Second); r.stop != "cancelled" || len(r.errs) != 0 {
		t.Fatalf("stop %q errors %q", r.stop, r.errs)
	}
	e.m.SetWaker(wake)
	e.ok("again")
}

func TestCancelWhileWaitingForStartup(t *testing.T) {
	e := newEnv(t)
	e.script("initialize hang")
	first := e.start("first")
	for len(e.pids()) == 0 { // the first turn holds the startup lock in initialize
		time.Sleep(10 * time.Millisecond)
	}
	s2, err := e.m.Create(context.Background(), agentID, "u_1", "")
	if err != nil {
		t.Fatal(err)
	}
	e2 := *e
	e2.sid = s2.ID
	second := e2.start("second")
	if err := e.m.Cancel(context.Background(), s2.ID); err != nil {
		t.Fatal(err)
	}
	if r := e2.wait(second, 5*time.Second); r.stop != "cancelled" {
		t.Fatalf("waiting turn: stop %q errors %q", r.stop, r.errs)
	}
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	if r := e.wait(first, 5*time.Second); r.stop != "cancelled" {
		t.Fatalf("starting turn: stop %q errors %q", r.stop, r.errs)
	}
	e.ok("again")
}

func TestPermissionAfterCancel(t *testing.T) {
	for _, policy := range []string{PolicyAsk, PolicyAutoApprove} {
		t.Run(policy, func(t *testing.T) {
			e := newEnv(t)
			if err := e.st.SetPermissionPolicy(context.Background(), db.SetPermissionPolicyParams{PermissionPolicy: policy, UpdatedAt: 1, ID: agentID}); err != nil {
				t.Fatal(err)
			}
			// The harness asks for a permission after it got session/cancel:
			// the answer must be "cancelled", right away.
			sub := e.startAndWait("permafter")
			if err := e.m.Cancel(context.Background(), e.sid); err != nil {
				t.Fatal(err)
			}
			r := e.wait(sub, 5*time.Second)
			if r.stop != "cancelled" || len(r.notices) != 0 || strings.Contains(r.text, "granted") {
				t.Fatalf("stop %q notices %q text %q", r.stop, r.notices, r.text)
			}
			if n := len(e.pids()); n != 1 {
				t.Fatalf("the harness was restarted (%d processes)", n)
			}
		})
	}
}

// second returns an env for another session of the same agent.
func (e *env) second() *env {
	e.t.Helper()
	s, err := e.m.Create(context.Background(), agentID, "u_1", "")
	if err != nil {
		e.t.Fatal(err)
	}
	e2 := *e
	e2.sid = s.ID
	return &e2
}

func TestCancelIgnoredWhileOthersRun(t *testing.T) {
	setTimeout(t, &cancelGrace, 300*time.Millisecond)
	e := newEnv(t)
	other := e.second()
	busy := other.startAndWait("slow 15") // 3 s of output
	stuck := e.startAndWait("hang")
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	r := e.wait(stuck, 5*time.Second)
	if r.stop != "cancelled" || len(r.notices) != 1 || !strings.Contains(r.notices[0], "other conversations") {
		t.Fatalf("stuck turn: stop %q errors %q notices %q", r.stop, r.errs, r.notices)
	}
	// The other conversation is not touched.
	if r := other.wait(busy, 10*time.Second); r.stop != "end_turn" || !strings.Contains(r.text, "chunk 15") {
		t.Fatalf("other turn: stop %q errors %q", r.stop, r.errs)
	}
	// Then the wedged harness goes, and the next turn gets a fresh one.
	e.waitDead(e.pids()[0])
	e.ok("again")
	if n := len(e.pids()); n != 2 {
		t.Fatalf("%d harness processes", n)
	}
}

func TestSetupTimeoutWhileOthersRun(t *testing.T) {
	setTimeout(t, &setupIdle, 500*time.Millisecond)
	setTimeout(t, &cancelGrace, 300*time.Millisecond)
	e := newEnv(t)
	other := e.second()
	busy := other.startAndWait("hang") // a live turn, silent
	e.script("session/new hang")
	r := e.turn("hello")
	e.expectError(r, "did not answer session/new", "this turn was stopped, and the harness restarts once the agent's other conversations are done")
	if !e.m.Running(other.sid) {
		t.Fatal("the other conversation's turn was ended")
	}
	if !alive(e.pids()[0]) {
		t.Fatal("the harness was stopped under the other conversation")
	}
	if err := e.m.Cancel(context.Background(), other.sid); err != nil {
		t.Fatal(err)
	}
	other.wait(busy, 5*time.Second)
	e.waitDead(e.pids()[0])
	e.ok("again")
}

func TestUpdateCounter(t *testing.T) {
	var seen atomic.Int64
	stream := `{"jsonrpc":"2.0","method":"session/update","params":{"x":1}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"session/update","params":{}}` + "\n" + // a request, not a notification
		`{"jsonrpc":"2.0","method":"session/update","params":{"text":"session/update"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"other","params":{"text":"session/update"}}` + "\n" +
		`{"jsonrpc":"2.0","params":{},"method": "session/update"}` + "\n" +
		`{"jsonrpc":"2.0","method":"session/update"` // unfinished: not counted
	// One byte per read, so every line straddles reads.
	u := &updateCounter{r: iotest.OneByteReader(strings.NewReader(stream)), seen: &seen}
	if _, err := io.Copy(io.Discard, u); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load(); got != 3 {
		t.Fatalf("counted %d session/update notifications, want 3", got)
	}
}
