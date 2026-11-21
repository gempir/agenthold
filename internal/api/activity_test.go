package api_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
)

func TestAgentActivityShowsWakeAndHarnessStart(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess)
	st := e.stream(alice, sess.Id, 0)
	alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hello"}, nil)
	st.until(isType("turn_end"))
	e.sessions.Wait()

	var entries []gen.ActivityEntry
	expect(t, "activity", alice.do("GET", "/agents/"+ag.Id+"/activity", nil, &entries), 200)
	var all []string
	for _, en := range entries {
		all = append(all, en.Source+": "+en.Message)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"wake: sandboxd connected", "harness: process started", "harness: initialize answered", "harness: session/new answered", "harness: prompt sent", "harness: first update of the turn"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("activity lacks %q:\n%s", want, joined)
		}
	}
	last := entries[len(entries)-1].Seq
	var none []gen.ActivityEntry
	expect(t, "activity after", alice.do("GET", "/agents/"+ag.Id+"/activity?after="+strconv.FormatInt(last, 10), nil, &none), 200)
	if len(none) != 0 {
		t.Fatalf("entries after the last: %+v", none)
	}
	expect(t, "stranger", bob.do("GET", "/agents/"+ag.Id+"/activity", nil, nil), 404)
}
