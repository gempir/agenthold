package api_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

// count is how many calls so far match pred.
func (f *fakeSlack) count(pred func(slackCall) bool) int {
	n := 0
	for _, c := range f.all() {
		if pred(c) {
			n++
		}
	}
	return n
}

// waitCount waits until n calls match pred.
func (f *fakeSlack) waitCount(what string, n int, pred func(slackCall) bool) {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for f.count(pred) < n {
		if time.Now().After(deadline) {
			f.t.Fatalf("fewer than %d Slack calls: %s (%d)", n, what, f.count(pred))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// threadsOf waits for the session to have n bound threads.
func (se *slackEnv) threadsOf(sessionID string, n int) []db.SlackThread {
	se.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, err := se.store.ListSlackThreadsOfSession(se.t.Context(), sessionID)
		if err == nil && len(rows) == n {
			return rows
		}
		if time.Now().After(deadline) {
			se.t.Fatalf("session %s has %d threads, want %d (%v)", sessionID, len(rows), n, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (se *slackEnv) webPrompt(c *client, sessionID, text string) {
	se.t.Helper()
	se.waitIdle(sessionID)
	expect(se.t, "web prompt "+text, c.do("POST", "/sessions/"+sessionID+"/prompt", map[string]string{"text": text}, nil), 202)
}

// A thread shows its session as the person who bound it: once they lose
// access to it, or disconnect Slack, it stops.
func TestSlackThreadStopsWhenAccessEnds(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	bob := se.login("bob", "Bob", "b@example.com")
	ag := se.fakeAgent(alice, "")
	expect(t, "share", alice.do("POST", "/agents/"+ag.Id+"/shares", gen.ShareInput{Email: "b@example.com"}, nil), 200)
	se.link(alice, "U1")
	se.link(bob, "U2")
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{}, &sess), 201)
	se.webPrompt(alice, sess.Id, "hello")

	// Bob continues alice's chat in #general, alice in #private.
	se.click("U2", "C9", slackgo.BlockAction{ActionID: "continue", Value: sess.Id})
	se.threadsOf(sess.Id, 1)
	se.click("U1", "C8", slackgo.BlockAction{ActionID: "continue", Value: sess.Id})
	se.threadsOf(sess.Id, 2)
	inChannel := func(channel string, parts ...string) func(slackCall) bool {
		return func(c slackCall) bool {
			return c.params.Get("channel") == channel && shows(c.method, parts...)(c)
		}
	}
	se.webPrompt(alice, sess.Id, "while shared")
	se.fake.waitFor("mirrored to bob's thread", inChannel("C9", "*Alice* (web): while shared"))
	se.fake.waitFor("answer in bob's thread", func(c slackCall) bool {
		return c.method == "chat.update" && inChannel("C9", "echo: while shared", "Done")(c)
	})
	se.fake.waitFor("answer in alice's thread", func(c slackCall) bool {
		return c.method == "chat.update" && inChannel("C8", "echo: while shared", "Done")(c)
	})

	// Alice stops sharing: her next prompt does not reach bob's thread.
	expect(t, "unshare", alice.do("DELETE", "/agents/"+ag.Id+"/shares/"+bob.me().Id, nil, nil), 204)
	se.webPrompt(alice, sess.Id, "after unshare")
	se.fake.waitFor("notice in bob's thread", inChannel("C9", "no longer follows its chat", "*Bob*"))
	se.fake.waitFor("alice's thread goes on", func(c slackCall) bool {
		return c.method == "chat.update" && inChannel("C8", "echo: after unshare", "Done")(c)
	})
	if rows := se.threadsOf(sess.Id, 1); rows[0].ChannelID != "C8" {
		t.Fatalf("threads left %+v", rows)
	}

	// Alice disconnects Slack: her thread stops too.
	expect(t, "unlink", alice.do("DELETE", "/me/slack/T1/U1", nil, nil), 204)
	se.webPrompt(alice, sess.Id, "after unlink")
	se.fake.waitFor("notice in alice's thread", inChannel("C8", "no longer follows its chat"))
	se.threadsOf(sess.Id, 0)
	se.waitIdle(sess.Id)
	for _, c := range se.fake.all() {
		if (strings.Contains(c.text(), "after unshare") && c.params.Get("channel") == "C9") || strings.Contains(c.text(), "after unlink") {
			t.Fatalf("shown after access ended: %s %s %s", c.method, c.params.Get("channel"), c.text())
		}
	}
}

// A restart mid-turn leaves the turn's message showing Stop: the next
// process finishes it in place from the log.
func TestSlackResumesTurnAfterRestart(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "")
	se.link(alice, "U1")

	se.dm("U1", "700.1", "", "slow 8")
	row := se.thread("DU1", "700.1")
	se.fake.waitFor("running turn", shows("chat.update", "chunk 2", `"action_id":"stop"`))
	se.slack.Close()
	saved, err := se.store.GetSlackThread(t.Context(), db.GetSlackThreadParams{TeamID: "T1", ChannelID: "DU1", ThreadTs: "700.1"})
	if err != nil || saved.TurnSeq == 0 || saved.TurnTs == "" || saved.LastSeq == 0 {
		t.Fatalf("turn not persisted: %+v %v", saved, err)
	}
	se.waitIdle(row.SessionID) // the turn ends while no Slack service runs
	before := len(se.fake.all())

	se.slack = se.newService(true)
	final := se.fake.waitFor("finished in the old message", func(c slackCall) bool {
		return c.method == "chat.update" && c.params.Get("ts") == saved.TurnTs && strings.Contains(c.text(), "Done")
	})
	if !strings.Contains(final.text(), "chunk 1 chunk 2") || !strings.Contains(final.text(), "chunk 8 done") || strings.Contains(final.text(), `"action_id":"stop"`) {
		t.Fatalf("final message %s", final.text())
	}
	for _, c := range se.fake.all()[before:] {
		if c.method == "chat.postMessage" && strings.Contains(c.text(), "chunk") {
			t.Fatalf("the answer was posted again: %s", c.text())
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := se.store.GetSlackThread(t.Context(), db.GetSlackThreadParams{TeamID: "T1", ChannelID: "DU1", ThreadTs: "700.1"})
		if err == nil && r.TurnSeq == 0 && r.TurnTs == "" && r.LastSeq > saved.LastSeq {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cursor after the turn %+v %v", r, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// One long chunk of answer is split over as many messages as it takes.
func TestSlackLongAnswerSplits(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "")
	se.link(alice, "U1")

	se.dm("U1", "800.1", "", strings.Repeat("wörd ", 5000))
	se.fake.waitFor("answer", shows("chat.update", "Done"))
	parts := 0
	for _, c := range se.fake.all() {
		if c.method != "chat.postMessage" && c.method != "chat.update" {
			continue
		}
		var blocks []map[string]any
		_ = json.Unmarshal([]byte(c.params.Get("blocks")), &blocks)
		for _, b := range blocks {
			if b["type"] != "markdown" {
				continue
			}
			text, _ := b["text"].(string)
			if n := len([]rune(text)); n > 12000 {
				t.Fatalf("a markdown block of %d characters", n)
			}
			if !strings.HasPrefix(text, "echo") && !strings.HasPrefix(text, "wörd") {
				t.Fatalf("a part starts mid-word: %q", text[:20])
			}
			parts++
		}
	}
	if parts < 3 {
		t.Fatalf("%d parts", parts)
	}
}

// A final update Slack refuses is tried again.
func TestSlackRetriesRefusedUpdate(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "")
	se.link(alice, "U1")

	failed := false
	se.fake.failWith(func(c slackCall) string {
		if !failed && c.method == "chat.update" && strings.Contains(c.text(), "Done") {
			failed = true
			return "internal_error"
		}
		return ""
	})
	se.dm("U1", "900.1", "", "hello retry")
	se.fake.waitCount("final update tried again", 2, shows("chat.update", "echo: hello retry", "Done"))
}

// An unlinked person writing in a thread is asked to link once an hour, not
// for every message.
func TestSlackLinkPromptOncePerThread(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "")
	se.link(alice, "U1")
	se.mention("U1", "C1", "100.1", "", "hello")
	row := se.thread("C1", "100.1")
	se.waitIdle(row.SessionID)

	prompts := func(c slackCall) bool {
		return shows("chat.postEphemeral", "Connect your agenthold account")(c) && c.params.Get("user") == "U3"
	}
	se.reply("U3", "C1", "100.2", "100.1", "me too")
	se.reply("U3", "C1", "100.3", "100.1", "and again")
	if n := se.fake.count(prompts); n != 1 {
		t.Fatalf("%d link prompts", n)
	}
	se.clock.Advance(61 * time.Minute)
	se.reply("U3", "C1", "100.4", "100.1", "later")
	if n := se.fake.count(prompts); n != 2 {
		t.Fatalf("%d link prompts an hour later", n)
	}
}

// Concurrent reconfiguration leaves one connection, or none after removal.
func TestSlackReconfigureKeepsOneConnection(t *testing.T) {
	se := newSlackEnvWith(t, true)
	admin := se.login("admin", "Admin", "admin@example.com")
	tokens := gen.SlackTokens{BotToken: "xoxb-good", AppToken: "xapp-good"}
	connections := func() int {
		n := 0
		for _, g := range goroutines() {
			if strings.Contains(g, "internal/slack.(*Service).runSocket(") {
				n++
			}
		}
		return n
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			if i%4 == 3 {
				admin.do("DELETE", "/admin/slack", nil, nil)
			} else {
				admin.do("PUT", "/admin/slack", tokens, nil)
			}
		})
	}
	wg.Wait()
	expect(t, "configure", admin.do("PUT", "/admin/slack", tokens, nil), 200)
	if n := connections(); n != 1 {
		t.Fatalf("%d Socket Mode connections after reconfiguring", n)
	}
	expect(t, "delete", admin.do("DELETE", "/admin/slack", nil, nil), 204)
	if n := connections(); n != 0 {
		t.Fatalf("%d Socket Mode connections after removal", n)
	}
}
