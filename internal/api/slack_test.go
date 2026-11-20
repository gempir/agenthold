package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
	slackapp "github.com/gempir/agenthold/internal/slack"
	"github.com/gempir/agenthold/internal/store/db"
)

// fakeSlack is Slack's Web API, recording every call.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls []slackCall
	ts    int
}

type slackCall struct {
	method string
	params url.Values
	body   map[string]any // JSON requests (views.*) and response URLs
}

// text is everything a call showed: its text and blocks.
func (c slackCall) text() string {
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		return string(b)
	}
	return c.params.Get("text") + " " + c.params.Get("blocks")
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	call := slackCall{method: method}
	raw, _ := io.ReadAll(r.Body)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(raw, &call.body)
	} else {
		call.params, _ = url.ParseQuery(string(raw))
	}
	if call.params == nil {
		call.params = url.Values{}
	}
	for k, v := range r.URL.Query() {
		call.params[k] = v
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = call.params.Get("token")
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.ts++
	ts := fmt.Sprintf("1700000000.%06d", f.ts)
	f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/respond/") {
		_, _ = w.Write([]byte("ok"))
		return
	}
	out := map[string]any{"ok": true}
	switch method {
	case "auth.test":
		if token != "xoxb-good" {
			out = map[string]any{"ok": false, "error": "invalid_auth"}
			break
		}
		out = map[string]any{"ok": true, "team": "Acme", "team_id": "T1", "user": "agenthold", "user_id": "UBOT", "bot_id": "B1"}
	case "apps.connections.open":
		if token != "xapp-good" {
			out = map[string]any{"ok": false, "error": "invalid_auth"}
			break
		}
		out["url"] = "wss://slack.invalid/link"
	case "chat.postMessage":
		ch := call.params.Get("channel")
		if strings.HasPrefix(ch, "U") {
			ch = "D" + ch
		}
		if ch == "CNOPE" {
			out = map[string]any{"ok": false, "error": "not_in_channel"}
			break
		}
		out["channel"], out["ts"] = ch, ts
	case "chat.update":
		out["channel"], out["ts"] = call.params.Get("channel"), call.params.Get("ts")
	case "chat.postEphemeral":
		out["message_ts"] = ts
	case "users.info":
		out["user"] = map[string]any{"id": call.params.Get("user"), "name": "slack-" + call.params.Get("user"), "profile": map[string]any{"display_name": "Display " + call.params.Get("user")}}
	case "conversations.open":
		out["channel"] = map[string]any{"id": "D" + call.params.Get("users")}
	case "views.open", "views.update", "views.publish":
		out["view"] = map[string]any{"id": "V1"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeSlack) all() []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]slackCall(nil), f.calls...)
}

// waitFor waits for a call matching pred and returns it.
func (f *fakeSlack) waitFor(what string, pred func(slackCall) bool) slackCall {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, c := range f.all() {
			if pred(c) {
				return c
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, c := range f.all() {
				got = append(got, c.method+" "+truncateTest(c.text(), 300))
			}
			f.t.Fatalf("no Slack call: %s\ncalls:\n%s", what, strings.Join(got, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func truncateTest(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// shows matches a call of method whose text contains every part.
func shows(method string, parts ...string) func(slackCall) bool {
	return func(c slackCall) bool {
		if c.method != method {
			return false
		}
		t := c.text()
		for _, p := range parts {
			if !strings.Contains(t, p) {
				return false
			}
		}
		return true
	}
}

type slackEnv struct {
	*env
	slack *slackapp.Service
	fake  *fakeSlack
	clock *clock
}

// newSlackEnv is an environment with a connected Slack app (no socket:
// tests hand events to the service).
func newSlackEnv(t *testing.T) *slackEnv {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"admin@example.com"} })
	f := newFakeSlack(t)
	clk := &clock{t: time.Now()}
	svc := slackapp.New(slackapp.Options{
		Store: e.store, Box: e.box, API: e.api, Auth: e.auth, Sessions: e.sessions, PublicURL: e.srv.URL,
		APIURL: f.srv.URL + "/api/", NoSocket: true, Throttle: 20 * time.Millisecond, Now: clk.Now,
	})
	e.api.Slack = svc
	t.Cleanup(svc.Close)
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(t.Context(), "", "xoxb-good", "xapp-good"); err != nil {
		t.Fatal(err)
	}
	return &slackEnv{env: e, slack: svc, fake: f, clock: clk}
}

func (se *slackEnv) event(inner any) {
	e := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, TeamID: "T1", InnerEvent: slackevents.EventsAPIInnerEvent{Data: inner}}
	se.slack.HandleEvent(se.t.Context(), e)
}

func (se *slackEnv) dm(user, ts, threadTS, text string) {
	se.event(&slackevents.MessageEvent{Type: "message", User: user, Text: text, TimeStamp: ts, ThreadTimeStamp: threadTS, Channel: "D" + user, ChannelType: "im"})
}

func (se *slackEnv) mention(user, channel, ts, threadTS, text string) {
	se.event(&slackevents.AppMentionEvent{Type: "app_mention", User: user, Text: "<@UBOT> " + text, TimeStamp: ts, ThreadTimeStamp: threadTS, Channel: channel})
}

func (se *slackEnv) reply(user, channel, ts, threadTS, text string) {
	se.event(&slackevents.MessageEvent{Type: "message", User: user, Text: text, TimeStamp: ts, ThreadTimeStamp: threadTS, Channel: channel, ChannelType: "channel"})
}

// click sends a block action.
func (se *slackEnv) click(user, channel string, act slackgo.BlockAction) any {
	cb := slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeBlockActions, Team: slackgo.Team{ID: "T1"}, User: slackgo.User{ID: user},
		Channel:   slackgo.Channel{GroupConversation: slackgo.GroupConversation{Conversation: slackgo.Conversation{ID: channel}}},
		TriggerID: "trigger", ResponseURL: se.fake.srv.URL + "/respond/" + user,
		ActionCallback: slackgo.ActionCallbacks{BlockActions: []*slackgo.BlockAction{&act}},
	}
	return se.slack.HandleInteraction(se.t.Context(), cb)
}

// link connects a Slack user to the client's account through the link flow.
func (se *slackEnv) link(c *client, slackUser string) {
	se.t.Helper()
	code := se.linkCode(slackUser)
	expect(se.t, "link", c.do("POST", "/me/slack/links", map[string]string{"code": code}, nil), 200)
}

// linkCode makes the bot offer a link to slackUser and returns its code.
func (se *slackEnv) linkCode(slackUser string) string {
	se.t.Helper()
	before := len(se.fake.all())
	se.slack.HandleCommand(se.t.Context(), slackgo.SlashCommand{TeamID: "T1", ChannelID: "D" + slackUser, UserID: slackUser, Text: "link", ResponseURL: se.fake.srv.URL + "/respond/" + slackUser})
	calls := se.fake.all()[before:]
	for _, c := range calls {
		t := c.text()
		if i := strings.Index(t, "/slack/link?code="); i >= 0 {
			rest := t[i+len("/slack/link?code="):]
			end := strings.IndexAny(rest, `"&\`)
			code, _ := url.QueryUnescape(rest[:end])
			return code
		}
	}
	se.t.Fatalf("no link offered to %s", slackUser)
	return ""
}

func (se *slackEnv) thread(channel, ts string) db.SlackThread {
	se.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := se.store.GetSlackThread(se.t.Context(), db.GetSlackThreadParams{TeamID: "T1", ChannelID: channel, ThreadTs: ts})
		if err == nil {
			return row
		}
		if time.Now().After(deadline) {
			se.t.Fatalf("thread %s/%s not bound: %v", channel, ts, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSlackAdminEndpoints(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	admin := se.login("admin", "Admin", "admin@example.com")

	for _, op := range []struct{ method, path string }{{"GET", "/admin/slack"}, {"PUT", "/admin/slack"}, {"DELETE", "/admin/slack"}, {"GET", "/admin/slack/manifest"}} {
		expect(t, "non-admin "+op.method+" "+op.path, alice.do(op.method, op.path, gen.SlackTokens{BotToken: "xoxb-good", AppToken: "xapp-good"}, nil), 403)
	}
	var st gen.SlackSettings
	expect(t, "settings", admin.do("GET", "/admin/slack", nil, &st), 200)
	if !st.Configured || !st.Connected || *st.TeamName != "Acme" || *st.TeamId != "T1" || *st.BotUserId != "UBOT" {
		t.Fatalf("settings %+v", st)
	}
	row, err := se.store.GetSlackInstallation(t.Context())
	if err != nil || strings.Contains(string(row.BotToken), "xoxb") || strings.Contains(string(row.AppToken), "xapp") {
		t.Fatalf("tokens must be stored encrypted: %v", err)
	}

	status, body := admin.rawJSON("PUT", "/admin/slack", gen.SlackTokens{BotToken: "xoxb-bad", AppToken: "xapp-good"})
	if status != 400 || !strings.Contains(body, "invalid_auth") || !strings.Contains(body, "bot token") {
		t.Fatalf("bad bot token: %d %s", status, body)
	}
	status, body = admin.rawJSON("PUT", "/admin/slack", gen.SlackTokens{BotToken: "xoxb-good", AppToken: "xapp-bad"})
	if status != 400 || !strings.Contains(body, "app-level token") {
		t.Fatalf("bad app token: %d %s", status, body)
	}
	expect(t, "not a bot token", admin.do("PUT", "/admin/slack", gen.SlackTokens{BotToken: "xoxp-user", AppToken: "xapp-good"}, nil), 400)
	expect(t, "reconfigure", admin.do("PUT", "/admin/slack", gen.SlackTokens{BotToken: "xoxb-good", AppToken: "xapp-good"}, &st), 200)

	var manifest map[string]any
	expect(t, "manifest", admin.do("GET", "/admin/slack/manifest", nil, &manifest), 200)
	b, _ := json.Marshal(manifest)
	for _, want := range []string{`"socket_mode_enabled":true`, `"/agenthold"`, `"app_mentions:read"`, `"reactions:write"`, `"message.im"`, `"app_home_opened"`, `"home_tab_enabled":true`, `"is_enabled":true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("manifest lacks %s: %s", want, b)
		}
	}

	expect(t, "delete", admin.do("DELETE", "/admin/slack", nil, nil), 204)
	expect(t, "settings", admin.do("GET", "/admin/slack", nil, &st), 200)
	if st.Configured || st.Connected {
		t.Fatalf("after delete %+v", st)
	}
	// Without a connection, events are ignored.
	before := len(se.fake.all())
	se.dm("U1", "1.1", "", "hello")
	if n := len(se.fake.all()); n != before {
		t.Fatalf("an event was handled while disconnected (%d calls)", n-before)
	}
}

func TestSlackLinking(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	ag := se.fakeAgent(alice, "")

	// An unlinked user is offered the link, and nothing runs.
	se.dm("U1", "10.1", "", "hello agent")
	offer := se.fake.waitFor("link prompt", shows("chat.postEphemeral", "Connect your agenthold account", "/slack/link?code="))
	if offer.params.Get("user") != "U1" || offer.params.Get("channel") != "DU1" {
		t.Fatalf("link prompt went to %v", offer.params)
	}
	var sessions []gen.Session
	expect(t, "sessions", alice.do("GET", "/agents/"+ag.Id+"/sessions", nil, &sessions), 200)
	if len(sessions) != 0 {
		t.Fatalf("an unlinked user started a session: %+v", sessions)
	}

	code := se.linkCode("U1")
	var preview gen.SlackLinkPreview
	expect(t, "peek", alice.do("GET", "/me/slack/links/"+url.PathEscape(code), nil, &preview), 200)
	if preview.SlackUserId != "U1" || preview.SlackUserName != "Display U1" || preview.TeamName != "Acme" {
		t.Fatalf("preview %+v", preview)
	}
	expect(t, "peek again: not consumed", alice.do("GET", "/me/slack/links/"+url.PathEscape(code), nil, nil), 200)
	expect(t, "wrong code", alice.do("POST", "/me/slack/links", map[string]string{"code": code + "x"}, nil), 404)
	var id gen.SlackIdentity
	expect(t, "link", alice.do("POST", "/me/slack/links", map[string]string{"code": code}, &id), 200)
	if id.SlackUserId != "U1" || id.TeamId != "T1" {
		t.Fatalf("identity %+v", id)
	}
	expect(t, "used once", alice.do("POST", "/me/slack/links", map[string]string{"code": code}, nil), 404)
	se.fake.waitFor("confirmation DM", func(c slackCall) bool {
		return c.method == "chat.postMessage" && c.params.Get("channel") == "U1" && strings.Contains(c.text(), "connected to agenthold as")
	})
	var links []gen.SlackIdentity
	expect(t, "links", alice.do("GET", "/me/slack", nil, &links), 200)
	if len(links) != 1 || links[0].SlackUserName != "Display U1" {
		t.Fatalf("links %+v", links)
	}

	// Codes expire after 15 minutes.
	old := se.linkCode("U2")
	se.clock.Advance(16 * time.Minute)
	expect(t, "expired peek", alice.do("GET", "/me/slack/links/"+url.PathEscape(old), nil, nil), 404)
	expect(t, "expired link", alice.do("POST", "/me/slack/links", map[string]string{"code": old}, nil), 404)

	// Unlinking: others cannot, the owner can.
	bob := se.login("bob", "Bob", "b@example.com")
	expect(t, "bob unlinks alice's", bob.do("DELETE", "/me/slack/T1/U1", nil, nil), 404)
	expect(t, "unlink", alice.do("DELETE", "/me/slack/T1/U1", nil, nil), 204)
	expect(t, "links", alice.do("GET", "/me/slack", nil, &links), 200)
	if len(links) != 0 {
		t.Fatalf("links after unlink %+v", links)
	}
}

// waitIdle waits until the session has no running turn.
func (se *slackEnv) waitIdle(sessionID string) {
	se.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for se.sessions.Running(sessionID) {
		if time.Now().After(deadline) {
			se.t.Fatal("turn did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSlackMentionThreadAndMirror(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	ag := se.fakeAgent(alice, "")
	se.link(alice, "U1")

	// A mention starts a chat in a thread on the message.
	se.mention("U1", "C1", "100.1", "", "hello there")
	row := se.thread("C1", "100.1")
	if row.AgentID != ag.Id || row.HeaderTs == "" {
		t.Fatalf("thread %+v", row)
	}
	se.fake.waitFor("header", func(c slackCall) bool {
		return c.method == "chat.postMessage" && c.params.Get("thread_ts") == "100.1" && strings.Contains(c.text(), `"action_id":"model"`) && strings.Contains(c.text(), ag.Name)
	})
	se.fake.waitFor("finished answer", shows("chat.update", "echo: hello there", "Done"))
	// The thread's own message is not repeated.
	for _, c := range se.fake.all() {
		if c.method == "chat.postMessage" && strings.Contains(c.text(), "(web)") {
			t.Fatalf("own prompt echoed: %s", c.text())
		}
	}
	var sess gen.Session
	expect(t, "session", alice.do("GET", "/sessions/"+row.SessionID, nil, &sess), 200)
	if sess.Title != "hello there" {
		t.Fatalf("session %+v", sess)
	}
	se.waitIdle(row.SessionID)

	// A reply in the thread (no mention) continues the session.
	se.reply("U1", "C1", "100.2", "100.1", "second question")
	se.fake.waitFor("second answer", shows("chat.update", "echo: second question", "Done"))
	// Replies elsewhere are ignored.
	se.reply("U1", "C1", "200.2", "200.1", "not ours")
	se.reply("U1", "C1", "201.1", "", "top level")

	// A prompt from the web UI is mirrored with its author.
	se.waitIdle(row.SessionID)
	expect(t, "web prompt", alice.do("POST", "/sessions/"+row.SessionID+"/prompt", map[string]string{"text": "from the web"}, nil), 202)
	se.fake.waitFor("mirrored prompt", shows("chat.postMessage", "*Alice* (web): from the web"))
	se.fake.waitFor("mirrored answer", shows("chat.update", "echo: from the web", "Done"))
	for _, c := range se.fake.all() {
		if strings.Contains(c.text(), "not ours") || strings.Contains(c.text(), "top level") {
			t.Fatalf("an unbound message was handled: %s %s", c.method, c.text())
		}
	}

	// The audit log tells Slack's prompts apart.
	var entries []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &entries), 200)
	via := 0
	for _, en := range entries {
		if en.Action == "session.prompt" && en.Details["via"] == "slack" {
			via++
		}
	}
	if via != 2 {
		t.Fatalf("%d prompts recorded via slack, want 2: %+v", via, entries)
	}

	// Deleting the session in the web UI unbinds the thread.
	se.waitIdle(row.SessionID)
	expect(t, "delete", alice.do("DELETE", "/sessions/"+row.SessionID, nil, nil), 204)
	if _, err := se.store.GetSlackThread(t.Context(), db.GetSlackThreadParams{TeamID: "T1", ChannelID: "C1", ThreadTs: "100.1"}); err == nil {
		t.Fatal("binding survived the session")
	}
}

func TestSlackStopAndQueue(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "")
	se.link(alice, "U1")

	// DM: a top-level message starts a chat; Stop cancels it.
	se.dm("U1", "300.1", "", "slow 100")
	row := se.thread("DU1", "300.1")
	se.fake.waitFor("running turn with Stop", shows("chat.update", "chunk 1", `"action_id":"stop"`))
	se.click("U1", "DU1", slackgo.BlockAction{ActionID: "stop", Value: row.SessionID})
	se.fake.waitFor("cancelled footer", shows("chat.update", "Cancelled"))
	se.waitIdle(row.SessionID)

	// A message while a turn runs waits, with a reaction, and goes next.
	se.dm("U1", "300.2", "300.1", "slow 3")
	se.fake.waitFor("second turn", shows("chat.update", "chunk 1"))
	se.dm("U1", "300.3", "300.1", "queued one")
	se.fake.waitFor("waiting reaction", func(c slackCall) bool {
		return c.method == "reactions.add" && c.params.Get("timestamp") == "300.3" && c.params.Get("name") == "hourglass_flowing_sand"
	})
	se.fake.waitFor("reaction removed", func(c slackCall) bool {
		return c.method == "reactions.remove" && c.params.Get("timestamp") == "300.3"
	})
	se.fake.waitFor("queued answer", shows("chat.update", "echo: queued one", "Done"))
}

func TestSlackPermissionButtons(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	se.fakeAgent(alice, "ask")
	se.link(alice, "U1")

	se.mention("U1", "C1", "400.1", "", "write notes.txt hi there")
	row := se.thread("C1", "400.1")
	perm := se.fake.waitFor("permission request", shows("chat.postMessage", "Permission requested", "Write notes.txt", `"action_id":"perm_0"`))
	var blocks []map[string]any
	_ = json.Unmarshal([]byte(perm.params.Get("blocks")), &blocks)
	var allow slackgo.BlockAction
	for _, b := range blocks {
		els, _ := b["elements"].([]any)
		for _, el := range els {
			m := el.(map[string]any)
			if m["action_id"] == "perm_0" {
				if m["style"] != "primary" {
					t.Fatalf("allow button %v", m)
				}
				allow = slackgo.BlockAction{ActionID: "perm_0", Value: m["value"].(string)}
			}
		}
	}
	// Someone without access cannot answer.
	bob := se.login("bob", "Bob", "b@example.com")
	se.link(bob, "U2")
	se.click("U2", "C1", allow)
	se.fake.waitFor("bob refused", func(c slackCall) bool {
		return c.method == "respond/U2" && strings.Contains(c.text(), "Could not answer")
	})
	se.click("U1", "C1", allow)
	se.fake.waitFor("resolved", func(c slackCall) bool {
		return shows("chat.update", "Allowed", "*Alice*", "Write notes.txt")(c)
	})
	se.fake.waitFor("answer", shows("chat.update", "I wrote notes.txt", "Done"))
	se.waitIdle(row.SessionID)
}

// modalOptions lists the values of the model select in a views.open/update call.
func modalModels(c slackCall) []string {
	var out []string
	view, _ := c.body["view"].(map[string]any)
	blocks, _ := view["blocks"].([]any)
	for _, b := range blocks {
		m := b.(map[string]any)
		if m["block_id"] != "model" {
			continue
		}
		el := m["element"].(map[string]any)
		for _, o := range el["options"].([]any) {
			out = append(out, o.(map[string]any)["value"].(string))
		}
	}
	return out
}

func (se *slackEnv) submitModel(user, sessionID, model string, opts map[string]string) any {
	values := map[string]map[string]slackgo.BlockAction{
		"model": {"model_select": {Type: "static_select", SelectedOption: slackgo.OptionBlockObject{Value: model}}},
	}
	for k, v := range opts {
		values["opt:"+k] = map[string]slackgo.BlockAction{"value": {Type: "static_select", SelectedOption: slackgo.OptionBlockObject{Value: v}}}
	}
	cb := slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeViewSubmission, Team: slackgo.Team{ID: "T1"}, User: slackgo.User{ID: user},
		View: slackgo.View{CallbackID: "model", PrivateMetadata: `{"s":"` + sessionID + `"}`, State: &slackgo.ViewState{Values: values}},
	}
	return se.slack.HandleInteraction(se.t.Context(), cb)
}

func TestSlackModelModal(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	bob := se.login("bob", "Bob", "b@example.com")
	ag := se.fakeAgent(alice, "")
	expect(t, "share", alice.do("POST", "/agents/"+ag.Id+"/shares", gen.ShareInput{Email: "b@example.com"}, nil), 200)
	se.link(alice, "U1")
	se.link(bob, "U2")

	// Bob starts a chat before the harness ever reported its models.
	se.dm("U2", "500.1", "", "hello")
	row := se.thread("DU2", "500.1")
	se.fake.waitFor("answer", shows("chat.update", "echo: hello", "Done"))
	se.waitIdle(row.SessionID)

	// The first turn reported the catalog: bob chooses among its models.
	n := len(se.fake.all())
	se.click("U2", "DU2", slackgo.BlockAction{ActionID: "model", Value: row.SessionID})
	open := se.fake.waitFor("model modal", func(c slackCall) bool { return c.method == "views.open" })
	if got := strings.Join(modalModels(open), ","); got != "default,swift-1,deep-1,tiny-1,broken-1" {
		t.Fatalf("models offered %s (%d calls before)", got, n)
	}
	if !strings.Contains(open.text(), "opt:effort") {
		t.Fatalf("no effort option: %s", open.text())
	}
	if res := se.submitModel("U2", row.SessionID, "deep-1", map[string]string{"effort": "high"}); res != nil {
		t.Fatalf("bob's choice refused: %+v", res)
	}
	var sess gen.Session
	expect(t, "session", alice.do("GET", "/sessions/"+row.SessionID, nil, &sess), 200)
	if sess.Model == nil || sess.Model.Model != "deep-1" || (*sess.Model.Options)["effort"] != "high" {
		t.Fatalf("session model %+v", sess.Model)
	}
	se.fake.waitFor("header shows the model", shows("chat.update", "Deep 1 · High"))
	// A model the harness did not report is the owner's to choose.
	res := se.submitModel("U2", row.SessionID, "custom-9", nil)
	vr, ok := res.(*slackgo.ViewSubmissionResponse)
	if !ok || vr.ResponseAction != slackgo.RAErrors || !strings.Contains(vr.Errors["model"], "owner") {
		t.Fatalf("bob's custom model: %+v", res)
	}
	if res := se.submitModel("U1", row.SessionID, "custom-9", nil); res != nil {
		t.Fatalf("alice's custom model refused: %+v", res)
	}
	// Selecting another model in the modal redraws its options.
	se.slack.HandleInteraction(t.Context(), slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeBlockActions, Team: slackgo.Team{ID: "T1"}, User: slackgo.User{ID: "U2"},
		Container: slackgo.Container{Type: "view", ViewID: "V1"},
		View:      slackgo.View{ID: "V1", Hash: "h1", Type: slackgo.VTModal, CallbackID: "model", PrivateMetadata: `{"s":"` + row.SessionID + `"}`},
		ActionCallback: slackgo.ActionCallbacks{BlockActions: []*slackgo.BlockAction{{
			ActionID: "model_select", BlockID: "model", SelectedOption: slackgo.OptionBlockObject{Value: "tiny-1"},
		}}},
	})
	se.fake.waitFor("modal redrawn", func(c slackCall) bool { return c.method == "views.update" && c.body["view_id"] == "V1" })
	// swift-1's own options include fast mode.
	se.slack.HandleInteraction(t.Context(), slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeBlockActions, Team: slackgo.Team{ID: "T1"}, User: slackgo.User{ID: "U1"},
		Container: slackgo.Container{Type: "view", ViewID: "V2"},
		View:      slackgo.View{ID: "V2", Hash: "h2", Type: slackgo.VTModal, CallbackID: "model", PrivateMetadata: `{"s":"` + row.SessionID + `"}`},
		ActionCallback: slackgo.ActionCallbacks{BlockActions: []*slackgo.BlockAction{{
			ActionID: "model_select", BlockID: "model", SelectedOption: slackgo.OptionBlockObject{Value: "swift-1"},
		}}},
	})
	se.fake.waitFor("swift-1 options", func(c slackCall) bool {
		return c.method == "views.update" && c.body["view_id"] == "V2" && strings.Contains(c.text(), "opt:fast") && strings.Contains(c.text(), "opt:effort")
	})
}

func TestSlackModelModalBeforeReport(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	bob := se.login("bob", "Bob", "b@example.com")
	ag := se.fakeAgent(alice, "")
	expect(t, "share", alice.do("POST", "/agents/"+ag.Id+"/shares", gen.ShareInput{Email: "b@example.com"}, nil), 200)
	se.link(bob, "U2")
	se.slack.HandleCommand(t.Context(), slackgo.SlashCommand{TeamID: "T1", ChannelID: "DU2", UserID: "U2", Text: "new " + ag.Name, ResponseURL: se.fake.srv.URL + "/respond/U2"})
	header := se.fake.waitFor("header as thread root", func(c slackCall) bool {
		return c.method == "chat.postMessage" && c.params.Get("thread_ts") == "" && strings.Contains(c.text(), `"action_id":"model"`)
	})
	var blocks []map[string]any
	_ = json.Unmarshal([]byte(header.params.Get("blocks")), &blocks)
	sessionID := ""
	for _, b := range blocks {
		els, _ := b["elements"].([]any)
		for _, el := range els {
			if m := el.(map[string]any); m["action_id"] == "model" {
				sessionID = m["value"].(string)
			}
		}
	}
	row := db.SlackThread{SessionID: sessionID}
	se.click("U2", "DU2", slackgo.BlockAction{ActionID: "model", Value: row.SessionID})
	open := se.fake.waitFor("model modal", func(c slackCall) bool { return c.method == "views.open" })
	if !strings.Contains(open.text(), "owner chooses the model") || strings.Contains(open.text(), "model_select") {
		t.Fatalf("a shared user may not choose before the harness reported: %s", open.text())
	}
}

func TestSlackAgentPickerAndDefault(t *testing.T) {
	se := newSlackEnv(t)
	alice := se.login("alice", "Alice", "a@example.com")
	var a1, a2 gen.Agent
	expect(t, "agent 1", alice.do("POST", "/agents", gen.AgentInput{Name: "alpha", Harness: ptr("fake")}, &a1), 201)
	expect(t, "agent 2", alice.do("POST", "/agents", gen.AgentInput{Name: "beta", Harness: ptr("fake")}, &a2), 201)
	se.sandboxes.Wait()
	se.link(alice, "U1")

	// Two agents and no default: the message waits for a choice.
	se.dm("U1", "600.1", "", "pick me")
	picker := se.fake.waitFor("picker", shows("chat.postEphemeral", `"action_id":"pick"`, "alpha", "beta"))
	var blocks []map[string]any
	_ = json.Unmarshal([]byte(picker.params.Get("blocks")), &blocks)
	blockID := blocks[0]["block_id"].(string)
	cb := slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeBlockActions, Team: slackgo.Team{ID: "T1"}, User: slackgo.User{ID: "U1"},
		Channel: slackgo.Channel{GroupConversation: slackgo.GroupConversation{Conversation: slackgo.Conversation{ID: "DU1"}}}, ResponseURL: se.fake.srv.URL + "/respond/pick",
		ActionCallback: slackgo.ActionCallbacks{BlockActions: []*slackgo.BlockAction{{ActionID: "pick", BlockID: blockID, SelectedOption: slackgo.OptionBlockObject{Value: a2.Id}}}},
		BlockActionState: &slackgo.BlockActionStates{Values: map[string]map[string]slackgo.BlockAction{
			"pickdef:" + strings.TrimPrefix(blockID, "pick:"): {"pick_default": {SelectedOptions: []slackgo.OptionBlockObject{{Value: "default"}}}},
		}},
	}
	se.slack.HandleInteraction(t.Context(), cb)
	row := se.thread("DU1", "600.1")
	if row.AgentID != a2.Id {
		t.Fatalf("chat with %s, want beta", row.AgentID)
	}
	se.fake.waitFor("held message answered", shows("chat.update", "echo: pick me", "Done"))
	se.waitIdle(row.SessionID)

	// beta is now the DM's default: the next chat starts without asking.
	se.dm("U1", "601.1", "", "again")
	if r := se.thread("DU1", "601.1"); r.AgentID != a2.Id {
		t.Fatalf("default not used: %+v", r)
	}
	se.fake.waitFor("answer", shows("chat.update", "echo: again", "Done"))

	// /agenthold use switches it, by name, case-insensitively.
	se.slack.HandleCommand(t.Context(), slackgo.SlashCommand{TeamID: "T1", ChannelID: "DU1", UserID: "U1", Text: "use ALPHA", ResponseURL: se.fake.srv.URL + "/respond/use"})
	se.fake.waitFor("use answer", func(c slackCall) bool { return c.method == "respond/use" && strings.Contains(c.text(), "alpha") })
	se.dm("U1", "602.1", "", "third")
	if r := se.thread("DU1", "602.1"); r.AgentID != a1.Id {
		t.Fatalf("use did not switch the default: %+v", r)
	}
	se.fake.waitFor("answer", shows("chat.update", "echo: third", "Done"))
	se.waitIdle(se.thread("DU1", "602.1").SessionID)

	// /agenthold chats lists them; Continue in Slack binds a new thread.
	se.slack.HandleCommand(t.Context(), slackgo.SlashCommand{TeamID: "T1", ChannelID: "C9", UserID: "U1", Text: "chats", ResponseURL: se.fake.srv.URL + "/respond/chats"})
	list := se.fake.waitFor("chats", func(c slackCall) bool { return c.method == "respond/chats" })
	if !strings.Contains(list.text(), "Continue in Slack") || !strings.Contains(list.text(), "/a/"+a1.Id+"?s=") {
		t.Fatalf("chats %s", list.text())
	}
	se.click("U1", "C9", slackgo.BlockAction{ActionID: "continue", Value: row.SessionID})
	se.fake.waitFor("last answer", func(c slackCall) bool {
		return c.method == "chat.postMessage" && c.params.Get("channel") == "C9" && strings.Contains(c.text(), "Last answer") && strings.Contains(c.text(), "echo: pick me")
	})
	threads, err := se.store.ListSlackThreadsOfSession(t.Context(), row.SessionID)
	if err != nil || len(threads) != 2 {
		t.Fatalf("threads of the continued session: %+v %v", threads, err)
	}
}
