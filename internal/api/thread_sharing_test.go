package api_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/executor"
)

func TestThreadLevelSharingAuthzAndDiscovery(t *testing.T) {
	tm := newTeam(t)
	ag := tm.agent.Id

	var sharedSess, privateSess gen.Session
	expect(t, "create shared session", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{"title": "Multiplayer thread"}, &sharedSess), 201)
	expect(t, "create private session", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{"title": "Private thread"}, &privateSess), 201)

	// Seed a file inside the sandbox so we can verify read-only file access vs write denial.
	res := tm.alice.raw("PUT", "/agents/"+ag+"/files/content?path=hello.txt", strings.NewReader("hello world"))
	res.Body.Close()
	expect(t, "seed file", res.StatusCode, 204)

	// Share ONLY sharedSess: Bob as viewer, Carol as collaborator. Dave stays stranger.
	var bobShare gen.SessionShare
	expect(t, "share viewer", tm.alice.do("POST", "/sessions/"+sharedSess.Id+"/shares", map[string]string{
		"email": tm.emails["bob"], "role": "viewer",
	}, &bobShare), 200)
	if bobShare.Role != gen.ShareRoleViewer || bobShare.User.Id != tm.ids["bob"] {
		t.Fatalf("unexpected bob share: %+v", bobShare)
	}
	var carolShare gen.SessionShare
	expect(t, "share collaborator", tm.alice.do("POST", "/sessions/"+sharedSess.Id+"/shares", map[string]string{
		"email": tm.emails["carol"], "role": "collaborator",
	}, &carolShare), 200)
	if carolShare.Role != gen.ShareRoleCollaborator {
		t.Fatalf("unexpected carol share: %+v", carolShare)
	}

	// Non-owners cannot share threads.
	expect(t, "collaborator cannot share", tm.carol.do("POST", "/sessions/"+sharedSess.Id+"/shares", map[string]string{
		"email": tm.emails["dave"], "role": "viewer",
	}, nil), 403)
	expect(t, "stranger cannot share", tm.dave.do("POST", "/sessions/"+sharedSess.Id+"/shares", map[string]string{
		"email": tm.emails["dave"], "role": "viewer",
	}, nil), 404)

	// Discovery via GET /me/shared-threads.
	var bobThreads []gen.SharedThread
	expect(t, "bob shared threads", tm.bob.do("GET", "/me/shared-threads", nil, &bobThreads), 200)
	if len(bobThreads) != 1 || bobThreads[0].Session.Id != sharedSess.Id || bobThreads[0].Role != gen.ShareRoleViewer {
		t.Fatalf("unexpected bob shared threads: %+v", bobThreads)
	}
	var daveThreads []gen.SharedThread
	expect(t, "dave shared threads", tm.dave.do("GET", "/me/shared-threads", nil, &daveThreads), 200)
	if len(daveThreads) != 0 {
		t.Fatalf("dave should see 0 shared threads, got %+v", daveThreads)
	}

	// Thread isolation: Bob & Carol see ONLY sharedSess, never privateSess.
	var bobAgentSessions []gen.Session
	expect(t, "bob list agent sessions", tm.bob.do("GET", "/agents/"+ag+"/sessions", nil, &bobAgentSessions), 200)
	if len(bobAgentSessions) != 1 || bobAgentSessions[0].Id != sharedSess.Id {
		t.Fatalf("bob should only see sharedSess, got %+v", bobAgentSessions)
	}
	expect(t, "bob cannot read private session", tm.bob.do("GET", "/sessions/"+privateSess.Id, nil, nil), 404)
	expect(t, "carol cannot read private session", tm.carol.do("GET", "/sessions/"+privateSess.Id, nil, nil), 404)

	// Viewer capabilities: read session, events, files (read-only), and post review comments.
	var gotSess gen.Session
	expect(t, "viewer get session", tm.bob.do("GET", "/sessions/"+sharedSess.Id, nil, &gotSess), 200)
	if gotSess.ShareRole == nil || *gotSess.ShareRole != gen.ShareAccessViewer {
		t.Fatalf("expected shareRole=viewer, got %+v", gotSess.ShareRole)
	}
	expect(t, "viewer events", tm.bob.do("GET", "/sessions/"+sharedSess.Id+"/events", nil, nil), 200)
	expect(t, "viewer read file text", tm.bob.do("GET", "/agents/"+ag+"/files/read?path=hello.txt", nil, nil), 200)
	expect(t, "viewer list files", tm.bob.do("GET", "/agents/"+ag+"/files", nil, nil), 200)

	// Viewer restrictions: cannot prompt, queue, cancel, settle, write files, open terminal, or patch agent settings.
	expect(t, "viewer cannot prompt", tm.bob.do("POST", "/sessions/"+sharedSess.Id+"/prompt", map[string]string{"text": "hi"}, nil), 403)
	expect(t, "viewer cannot queue", tm.bob.do("POST", "/sessions/"+sharedSess.Id+"/queue", map[string]string{"text": "hi"}, nil), 403)
	expect(t, "viewer cannot settle", tm.bob.do("POST", "/sessions/"+sharedSess.Id+"/settle", nil, nil), 403)
	expect(t, "viewer cannot patch agent", tm.bob.do("PATCH", "/agents/"+ag, gen.AgentPatch{IdleMinutes: ptr(20)}, nil), 404)
	expect(t, "viewer cannot write file", tm.bob.do("POST", "/agents/"+ag+"/files/mkdir", map[string]string{"path": "nope"}, nil), 404)
	tr := tm.bob.raw("GET", "/agents/"+ag+"/terminal", nil)
	tr.Body.Close()
	expect(t, "viewer cannot open terminal", tr.StatusCode, 404)

	// Collaborator capabilities: prompt, queue, settle/unsettle; still NO terminal, NO file writes, NO agent settings, NO delete.
	expect(t, "collaborator prompt", tm.carol.do("POST", "/sessions/"+sharedSess.Id+"/prompt", map[string]string{"text": "hello from carol"}, nil), 202)
	tm.e.sessions.Wait()
	expect(t, "collaborator settle", tm.carol.do("POST", "/sessions/"+sharedSess.Id+"/settle", nil, nil), 200)
	expect(t, "collaborator unsettle", tm.carol.do("POST", "/sessions/"+sharedSess.Id+"/unsettle", nil, nil), 200)
	expect(t, "collaborator cannot delete session", tm.carol.do("DELETE", "/sessions/"+sharedSess.Id, nil, nil), 403)
	expect(t, "collaborator cannot patch agent", tm.carol.do("PATCH", "/agents/"+ag, gen.AgentPatch{IdleMinutes: ptr(20)}, nil), 404)
	expect(t, "collaborator cannot write file", tm.carol.do("POST", "/agents/"+ag+"/files/mkdir", map[string]string{"path": "nope"}, nil), 404)
	tr2 := tm.carol.raw("GET", "/agents/"+ag+"/terminal", nil)
	tr2.Body.Close()
	expect(t, "collaborator cannot open terminal", tr2.StatusCode, 404)

	// Viewer can leave the thread themselves.
	expect(t, "bob cannot remove carol", tm.bob.do("DELETE", "/sessions/"+sharedSess.Id+"/shares/"+tm.ids["carol"], nil, nil), 403)
	expect(t, "bob leaves thread", tm.bob.do("DELETE", "/sessions/"+sharedSess.Id+"/shares/"+tm.ids["bob"], nil, nil), 204)
	expect(t, "bob loses access", tm.bob.do("GET", "/sessions/"+sharedSess.Id, nil, nil), 404)
}

func TestGuestLinksApprovalQueueAndRevocation(t *testing.T) {
	tm := newTeam(t)
	ag := tm.agent.Id
	var sess gen.Session
	expect(t, "create session", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{"title": "Guest demo"}, &sess), 201)
	expect(t, "share bob viewer", tm.alice.do("POST", "/sessions/"+sess.Id+"/shares", map[string]string{"email": tm.emails["bob"], "role": "viewer"}, nil), 200)
	expect(t, "share carol collaborator", tm.alice.do("POST", "/sessions/"+sess.Id+"/shares", map[string]string{"email": tm.emails["carol"], "role": "collaborator"}, nil), 200)

	// Seed an initial prompt so the guest sees history.
	expect(t, "initial prompt", tm.alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "initial hello"}, nil), 202)
	tm.e.sessions.Wait()

	// Only owner can mint guest links.
	expect(t, "collaborator cannot create guest link", tm.carol.do("POST", "/sessions/"+sess.Id+"/guest-links", nil, nil), 403)
	var created gen.GuestLinkCreated
	expect(t, "create guest link", tm.alice.do("POST", "/sessions/"+sess.Id+"/guest-links", nil, &created), 201)
	if created.Token == "" || created.Url != "/guest/"+created.Token || !created.Link.Active {
		t.Fatalf("unexpected guest link: %+v", created)
	}

	// Open owner WebSocket stream to observe guest_request broadcast.
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ownerWSURL := "ws" + strings.TrimPrefix(tm.e.srv.URL, "http") + "/api/v1/sessions/" + sess.Id + "/stream?after=999999"
	ownerWS, resp, err := websocket.Dial(ctx, ownerWSURL, &websocket.DialOptions{
		HTTPClient: tm.alice.http, HTTPHeader: http.Header{"Origin": {tm.e.srv.URL}},
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ownerWS.CloseNow() }()

	// Public unauthenticated guest fetches GET /api/v1/guest/{token}.
	anon := tm.e.anon()
	var view struct {
		SessionId       string             `json:"sessionId"`
		Title           string             `json:"title"`
		AgentName       string             `json:"agentName"`
		Events          []gen.SessionEvent `json:"events"`
		PendingRequests []gen.GuestRequest `json:"pendingRequests"`
	}
	expect(t, "guest view", anon.do("GET", "/guest/"+created.Token, nil, &view), 200)
	if view.SessionId != sess.Id || len(view.Events) == 0 {
		t.Fatalf("unexpected guest view: %+v", view)
	}

	// Public guest submits a message request.
	var greq gen.GuestRequest
	expect(t, "guest request", anon.do("POST", "/guest/"+created.Token+"/requests", map[string]string{
		"name": "External Reviewer",
		"text": "Please run the unit tests",
	}, &greq), 201)
	if greq.Status != gen.GuestRequestPending || greq.GuestName != "External Reviewer" {
		t.Fatalf("unexpected guest request: %+v", greq)
	}

	// Verify owner WebSocket received the live guest_request frame!
	sawGuestFrame := false
	for !sawGuestFrame {
		var frame map[string]any
		if err := wsjson.Read(ctx, ownerWS, &frame); err != nil {
			t.Fatalf("waiting for guest_request frame: %v", err)
		}
		if frame["type"] == "guest_request" {
			sawGuestFrame = true
		}
	}

	// Viewer cannot approve; collaborator CAN approve & trigger turn.
	expect(t, "viewer cannot approve", tm.bob.do("POST", "/sessions/"+sess.Id+"/guest-requests/"+greq.Id+"/approve", nil, nil), 403)
	var approved gen.GuestRequest
	expect(t, "collaborator approves", tm.carol.do("POST", "/sessions/"+sess.Id+"/guest-requests/"+greq.Id+"/approve", nil, &approved), 200)
	if approved.Status != gen.GuestRequestApproved {
		t.Fatalf("expected approved status, got %+v", approved)
	}
	tm.e.sessions.Wait()

	// Verify the approved guest prompt is in the event log attributed to Guest (External Reviewer).
	var events []gen.SessionEvent
	expect(t, "events after approve", tm.alice.do("GET", "/sessions/"+sess.Id+"/events", nil, &events), 200)
	foundGuestPrompt := false
	for _, ev := range events {
		if ev.Type == gen.SessionEventTypePrompt && ev.Data["text"] == "Please run the unit tests" {
			if userMap, ok := ev.Data["user"].(map[string]any); ok {
				if name, _ := userMap["name"].(string); strings.Contains(name, "Guest (External Reviewer)") {
					foundGuestPrompt = true
				}
			}
		}
	}
	if !foundGuestPrompt {
		t.Fatalf("expected guest prompt attributed to Guest (External Reviewer) in events: %+v", events)
	}

	// Second guest request dismissed by owner.
	var greq2 gen.GuestRequest
	expect(t, "guest request 2", anon.do("POST", "/guest/"+created.Token+"/requests", map[string]string{"text": "ignore me"}, &greq2), 201)
	var dismissed gen.GuestRequest
	expect(t, "owner dismisses", tm.alice.do("POST", "/sessions/"+sess.Id+"/guest-requests/"+greq2.Id+"/dismiss", nil, &dismissed), 200)
	if dismissed.Status != gen.GuestRequestDismissed {
		t.Fatalf("expected dismissed, got %+v", dismissed)
	}

	// Instant revoke invalidates guest link immediately.
	expect(t, "revoke guest link", tm.alice.do("DELETE", "/sessions/"+sess.Id+"/guest-links/"+created.Link.Id, nil, nil), 204)
	expect(t, "revoked guest view 404", anon.do("GET", "/guest/"+created.Token, nil, nil), 404)
	expect(t, "revoked guest request 404", anon.do("POST", "/guest/"+created.Token+"/requests", map[string]string{"text": "too late"}, nil), 404)

	// 24h expiry check.
	tm.e.api.Now = tm.e.clock.Now
	var expLink gen.GuestLinkCreated
	expect(t, "create expiring link", tm.alice.do("POST", "/sessions/"+sess.Id+"/guest-links", nil, &expLink), 201)
	expect(t, "active before 24h", anon.do("GET", "/guest/"+expLink.Token, nil, nil), 200)
	tm.e.clock.Advance(25 * time.Hour)
	expect(t, "expired after 24h", anon.do("GET", "/guest/"+expLink.Token, nil, nil), 404)
}

func TestPresenceReviewCommentsAndHandoff(t *testing.T) {
	tm := newTeam(t)
	ag := tm.agent.Id
	var sess gen.Session
	expect(t, "create session", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{"title": "Collab review"}, &sess), 201)
	expect(t, "share bob viewer", tm.alice.do("POST", "/sessions/"+sess.Id+"/shares", map[string]string{"email": tm.emails["bob"], "role": "viewer"}, nil), 200)
	expect(t, "share carol collaborator", tm.alice.do("POST", "/sessions/"+sess.Id+"/shares", map[string]string{"email": tm.emails["carol"], "role": "collaborator"}, nil), 200)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(tm.e.srv.URL, "http") + "/api/v1/sessions/" + sess.Id + "/stream?after=0"
	aliceWS, r1, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: tm.alice.http, HTTPHeader: http.Header{"Origin": {tm.e.srv.URL}}})
	if r1 != nil && r1.Body != nil {
		r1.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = aliceWS.CloseNow() }()

	bobWS, r2, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: tm.bob.http, HTTPHeader: http.Header{"Origin": {tm.e.srv.URL}}})
	if r2 != nil && r2.Body != nil {
		r2.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bobWS.CloseNow() }()

	// Bob sends typing presence_update; Alice receives it in a presence frame.
	if err := wsjson.Write(ctx, bobWS, map[string]string{"type": "presence_update", "activity": "typing"}); err != nil {
		t.Fatal(err)
	}
	waitForFrame := func(match func(map[string]any) bool) map[string]any {
		t.Helper()
		for {
			var f map[string]any
			if err := wsjson.Read(ctx, aliceWS, &f); err != nil {
				t.Fatalf("ws read: %v", err)
			}
			if match(f) {
				return f
			}
		}
	}
	waitForFrame(func(f map[string]any) bool {
		if f["type"] != "presence" {
			return false
		}
		vs, _ := f["viewers"].([]any)
		for _, raw := range vs {
			v, _ := raw.(map[string]any)
			if v["id"] == tm.ids["bob"] && v["activity"] == "typing" {
				return true
			}
		}
		return false
	})

	// Viewer (Bob) posts a collaborative diff review comment; Alice sees review_comments_updated live.
	var comment gen.SessionReviewComment
	expect(t, "viewer creates review comment", tm.bob.do("POST", "/sessions/"+sess.Id+"/review-comments", map[string]any{
		"filePath": "internal/auth.go", "startLine": 12, "endLine": 15, "side": "new", "body": "Handle nil token",
	}, &comment), 201)
	if comment.UserName != "Bob" || comment.StartLine != 12 || comment.EndLine != 15 {
		t.Fatalf("unexpected comment: %+v", comment)
	}
	waitForFrame(func(f map[string]any) bool {
		if f["type"] != "review_comments_updated" {
			return false
		}
		cs, _ := f["comments"].([]any)
		return len(cs) == 1
	})

	// Collaborator (Carol) resolves Bob's review comment.
	var resolved gen.SessionReviewComment
	expect(t, "carol resolves comment", tm.carol.do("POST", "/sessions/"+sess.Id+"/review-comments/"+comment.Id+"/resolve", nil, &resolved), 200)
	if resolved.ResolvedAt == nil {
		t.Fatal("expected resolvedAt to be set")
	}

	// Explicit handoff endpoint + @teammate mention in prompt.
	var handoff gen.SessionHandoffResult
	expect(t, "explicit handoff", tm.alice.do("POST", "/sessions/"+sess.Id+"/handoff", map[string]string{
		"target": tm.emails["carol"], "note": "Please take over deployment",
	}, &handoff), 200)
	if handoff.To.Id != tm.ids["carol"] || !strings.Contains(handoff.Message, "@Carol") {
		t.Fatalf("unexpected handoff: %+v", handoff)
	}

	expect(t, "mention handoff in prompt", tm.carol.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{
		"text": "Fixed the lint issue, @Alice ready for final check",
	}, nil), 202)
	tm.e.sessions.Wait()

	var events []gen.SessionEvent
	expect(t, "events include handoffs", tm.alice.do("GET", "/sessions/"+sess.Id+"/events", nil, &events), 200)
	handoffCount := 0
	for _, ev := range events {
		if ev.Type == gen.SessionEventTypeNotice && ev.Data["kind"] == "handoff" {
			handoffCount++
		}
	}
	if handoffCount < 2 {
		t.Fatalf("expected at least 2 handoff notice events, got %d (%+v)", handoffCount, events)
	}
}

func TestPerCallerExecutorRouting(t *testing.T) {
	tm := newTeam(t)
	ag := tm.agent.Id
	var sess gen.Session
	expect(t, "create session", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{"title": "Shared exec"}, &sess), 201)
	expect(t, "share carol collaborator", tm.alice.do("POST", "/sessions/"+sess.Id+"/shares", map[string]string{
		"email": tm.emails["carol"], "role": "collaborator",
	}, nil), 200)

	// Build a lightweight fake Executor binary that echoes its own instance owner
	// (derived from EXECUTOR_DATA_DIR basename) on every tools/call response!
	fakeExecDir := t.TempDir()
	fakeExecSrc := filepath.Join(fakeExecDir, "main.go")
	fakeExecBin := filepath.Join(fakeExecDir, "fake-executor")
	src := `package main
import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)
func main() {
	owner := filepath.Base(os.Getenv("EXECUTOR_DATA_DIR"))
	var mu sync.Mutex
	sids := map[string]bool{}
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage ` + "`json:\"id\"`" + `
			Method string          ` + "`json:\"method\"`" + `
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method == "initialize" {
			mu.Lock()
			n++
			sid := fmt.Sprintf("%s-sid-%d", owner, n)
			sids[sid] = true
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", sid)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"owner": owner}})
			return
		}
		sid := r.Header.Get("Mcp-Session-Id")
		mu.Lock()
		ok := sids[sid]
		mu.Unlock()
		if !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"servedBy": owner, "sid": sid},
		})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	_, _ = os.Stdout.WriteString("{\"url\":\"http://" + ln.Addr().String() + "\"}\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = srv.Close()
}
`
	if err := os.WriteFile(fakeExecSrc, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", fakeExecBin, fakeExecSrc).CombinedOutput(); err != nil {
		t.Fatalf("build fake executor: %v\n%s", err, string(out))
	}

	mgr := executor.New(executor.Config{
		Binary:         fakeExecBin,
		Version:        "test",
		DataDir:        t.TempDir(),
		MasterKey:      bytes.Repeat([]byte{0x42}, 32),
		StartupTimeout: 10 * time.Second,
	})
	t.Cleanup(func() { _ = mgr.Close() })

	// 1) Verify ResolveExecutorCaller tracks the active prompting user ID on a running turn.
	// When idle -> defaults to agent owner (Alice).
	uid, err := api.ResolveExecutorCaller(t.Context(), tm.e.store, tm.e.sessions, ag, sess.Id)
	if err != nil || uid != tm.ids["alice"] {
		t.Fatalf("idle ResolveExecutorCaller = (%q, %v), want alice %q", uid, err, tm.ids["alice"])
	}

	// Start a slow turn as Carol (collaborator) and verify ResolveExecutorCaller returns Carol — NEVER Alice!
	expect(t, "carol slow prompt", tm.carol.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 8"}, nil), 202)
	uid, err = api.ResolveExecutorCaller(t.Context(), tm.e.store, tm.e.sessions, ag, sess.Id)
	if err != nil || uid != tm.ids["carol"] {
		t.Fatalf("active collaborator ResolveExecutorCaller = (%q, %v), want carol %q", uid, err, tm.ids["carol"])
	}
	tm.e.sessions.Wait()

	// 2) Verify BridgeMCP routes Alice's turn to Alice's Executor instance, then routes
	// Carol's turn over the SAME MCP bridge to Carol's Executor instance (never leaking Alice's tokens),
	// and refuses calls when ResolveUser returns an error without falling back to Alice!
	var activeMu sync.Mutex
	currentCaller := tm.ids["alice"]
	var callerErr error

	cConn, sConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.BridgeMCP(t.Context(), executor.BridgeOptions{
			UserID:       tm.ids["alice"],
			AgentID:      ag,
			SessionID:    sess.Id,
			ApprovalMode: executor.ApprovalModel,
			ResolveUser: func(context.Context) (string, error) {
				activeMu.Lock()
				defer activeMu.Unlock()
				return currentCaller, callerErr
			},
		}, sConn)
	}()

	reader := bufio.NewReader(cConn)
	call := func(payload string) string {
		t.Helper()
		if _, err := cConn.Write([]byte(payload + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return line
	}

	initLine := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if !strings.Contains(initLine, tm.ids["alice"]) {
		t.Fatalf("expected Alice's Executor on init, got %s", initLine)
	}
	aliceCall := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github.me"}}`)
	if !strings.Contains(aliceCall, tm.ids["alice"]) {
		t.Fatalf("expected Alice's Executor on Alice's turn, got %s", aliceCall)
	}

	// Switch active turn to Carol on the SAME bridge!
	activeMu.Lock()
	currentCaller = tm.ids["carol"]
	activeMu.Unlock()
	carolCall := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.me"}}`)
	if !strings.Contains(carolCall, tm.ids["carol"]) || strings.Contains(carolCall, tm.ids["alice"]) {
		t.Fatalf("expected Carol's Executor (never Alice's!) on Carol's turn, got %s", carolCall)
	}

	// If ResolveUser fails, BridgeMCP must refuse without falling back to Alice!
	activeMu.Lock()
	currentCaller = ""
	callerErr = errors.New("collaborator revoked")
	activeMu.Unlock()
	deniedCall := call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"github.me"}}`)
	if !strings.Contains(deniedCall, "unauthorized") || strings.Contains(deniedCall, tm.ids["alice"]) {
		t.Fatalf("expected unauthorized error without owner fallback, got %s", deniedCall)
	}

	_ = cConn.Close()
	<-done
	_, _ = io.Discard.Write(nil)
}
