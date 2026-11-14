package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
)

func (e *env) tick() {
	e.t.Helper()
	if err := e.sleeper.Tick(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
	e.sandboxes.Wait()
}

func TestIdleAgentSleepsAndWakesWithHistory(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "") // idle timeout: 15 minutes
	var sess gen.Session
	alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess)
	st := e.stream(alice, sess.Id, 0)
	alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "remember the word banana"}, nil)
	st.until(isType("turn_end"))
	e.sessions.Wait()

	got := alice.agent(ag.Id)
	if got.SleepsAt == nil || got.LastActivityAt == nil || got.SleepsAt.Sub(*got.LastActivityAt) != 15*time.Minute {
		t.Fatalf("sleepsAt %v lastActivity %v", got.SleepsAt, got.LastActivityAt)
	}

	e.clock.Advance(14 * time.Minute)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("slept too early: %s", s)
	}
	e.clock.Advance(2 * time.Minute)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("idle agent not asleep: %s", s)
	}
	if got := alice.agent(ag.Id); got.SleepsAt != nil {
		t.Fatal("sleeping agent reports sleepsAt")
	}

	// A new prompt wakes it; the conversation continues with its history
	// (the harness loads the session; no reset notice).
	expect(t, "prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "what was the word"}, nil), 202)
	evs := st.until(isType("turn_end"))
	var notices []string
	for _, ev := range evs {
		if ev.Type == "notice" {
			notices = append(notices, ev.Data["message"].(string))
		}
	}
	if strings.Contains(strings.Join(notices, "|"), "fresh context") || !strings.Contains(strings.Join(notices, "|"), "Starting the sandbox") {
		t.Fatalf("notices %q", notices)
	}
	if !strings.Contains(text(evs), "echo: what was the word") {
		t.Fatalf("answer %q", text(evs))
	}
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("not woken: %s", s)
	}
}

func TestActivityKeepsAgentAwake(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	// An open terminal keeps it awake.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/agents/" + ag.Id + "/terminal"
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: alice.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	for e.tracker.Terminals(ag.Id) == 0 { // wait until the pty is open
		if _, _, err := ws.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Advance(time.Hour)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("slept with an open terminal: %s", s)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
	for e.tracker.Terminals(ag.Id) != 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// A running turn keeps it awake.
	var sess gen.Session
	alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess)
	st := e.stream(alice, sess.Id, 0)
	alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 10"}, nil)
	st.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })
	e.clock.Advance(time.Hour)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("slept during a turn: %s", s)
	}
	st.until(isType("turn_end"))
	e.sessions.Wait() // the turn is fully done (its end counts as activity)

	// After that, idle time counts from the end of the turn.
	e.clock.Advance(10 * time.Minute)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("slept 10 minutes after the turn: %s", s)
	}
	e.clock.Advance(6 * time.Minute)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("not asleep 16 minutes after the turn: %s", s)
	}
}
