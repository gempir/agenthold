// Command fakeagent is a scripted ACP agent for tests and for developing the
// UI without a real model. Prompts:
//
//	write <path> <text...>   edit a file (asks for permission first)
//	slow <n>                 stream n chunks, 200ms apart (cancellable)
//	think <text>             a thought, then an answer
//	whoami                   the account login it was given (env and file)
//	model                    the session's model, effort and fast mode (see models.go)
//	mode                     the session's mode
//	anything else            echo it back
//
// Failures, for tests of everything around a harness:
//
//	exit <code>              send a chunk, then exit with code
//	kill                     send a chunk, then SIGKILL itself (like the OOM killer)
//	hang                     never answer, ignore cancel
//	error                    answer the prompt with a JSON-RPC error
//	garbage                  print a line that is not JSON-RPC, then answer
//	bigline                  print a line longer than the client accepts
//	stderr <bytes>           write one stderr line of that size and 1000 short ones, then answer
//	closestdout              close stdout and keep running
//	spawn [setsid]           start a helper that keeps stdout open (in its own
//	                         session with setsid), then exit 0; its pid goes
//	                         to $HOME/.fakeagent/helper.pid
//	permafter                wait for session/cancel, then ask for a permission
//	                         and end as cancelled only if it is refused as cancelled
//
// Each prompt is appended to $HOME/.fakeagent/prompts when it arrives, so
// tests can wait until the harness has it.
//
// $HOME/.fakeagent/script scripts the protocol steps, one directive per line:
//
//	<step> <action> [arg]    fail at step, once (the line is removed when it fires)
//	                         step: start, initialize, authenticate, session/new, session/load,
//	                         session/set_config_option
//	                         action: exit <code>, kill, hang, error, auth (session/new: needs authenticate),
//	                         slow <seconds> (answer late, writing stderr all along),
//	                         quiet <seconds> (answer late, silently)
//	no-load                  do not advertise session/load
//	require-login            session/new needs a login (credentials file or FAKEAGENT_TOKEN)
//
// Each process appends its pid to $HOME/.fakeagent/pids. Sessions are
// stored under $HOME/.fakeagent so session/load replays them
// after a restart. "fakeagent login" is the terminal login method, and
// "fake-key" an agent method that authenticate accepts; an account login
// arrives as FAKEAGENT_TOKEN or $HOME/.fakeagent/account.json.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type agent struct {
	conn    *acp.AgentSideConnection
	out     *lockedWriter // stdout, shared with the connection
	dir     string
	authed  bool // authenticate succeeded in this process
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	mcp     map[string][]string // session -> MCP servers it was given
	servers map[string][]acp.McpServer
	configs map[string]sessionConfig // session -> model choice
}

func (a *agent) platformServer(sid acp.SessionId) *acp.McpServerStdio {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.servers[string(sid)] {
		if s.Stdio != nil && s.Stdio.Name == "agenthold" {
			return s.Stdio
		}
	}
	return nil
}

func mcpNames(servers []acp.McpServer) []string {
	var out []string
	for _, s := range servers {
		switch {
		case s.Stdio != nil:
			env := []string{}
			for _, e := range s.Stdio.Env {
				env = append(env, e.Name+"="+e.Value)
			}
			out = append(out, "stdio:"+s.Stdio.Name+":"+s.Stdio.Command+":"+strings.Join(env, ","))
		case s.Http != nil:
			h := []string{}
			for _, x := range s.Http.Headers {
				h = append(h, x.Name+"="+x.Value)
			}
			out = append(out, "http:"+s.Http.Name+":"+s.Http.Url+":"+strings.Join(h, ","))
		}
	}
	return out
}

var (
	_ acp.Agent       = (*agent)(nil)
	_ acp.AgentLoader = (*agent)(nil)
)

func main() {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".fakeagent")
	_ = os.MkdirAll(filepath.Join(dir, "sessions"), 0o755)
	if len(os.Args) > 1 && os.Args[1] == "login" {
		login(dir)
		return
	}
	// Every harness process leaves its pid, so tests can check that none outlives its use.
	if f, err := os.OpenFile(filepath.Join(dir, "pids"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, os.Getpid())
		_ = f.Close()
	}
	a := &agent{dir: dir, out: &lockedWriter{w: os.Stdout}, cancels: map[string]context.CancelFunc{}, mcp: map[string][]string{}, servers: map[string][]acp.McpServer{}, configs: map[string]sessionConfig{}}
	if action, arg, ok := a.scripted("start"); ok {
		_ = a.fail("start", action, arg)
	}
	a.conn = acp.NewAgentSideConnection(a, a.out, os.Stdin)
	fmt.Fprintln(os.Stderr, "fakeagent ready")
	<-a.conn.Done()
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func (a *agent) scriptPath() string { return filepath.Join(a.dir, "script") }

// scripted returns the first directive for step and removes it from the script.
func (a *agent) scripted(step string) (action, arg string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := os.ReadFile(a.scriptPath())
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != step {
			continue
		}
		rest := slices.Delete(lines, i, i+1)
		_ = os.WriteFile(a.scriptPath(), []byte(strings.Join(rest, "\n")+"\n"), 0o644)
		if len(f) > 2 {
			arg = f[2]
		}
		return f[1], arg, true
	}
	return "", "", false
}

// flag reports whether the script has a line that is exactly name.
func (a *agent) flag(name string) bool {
	b, _ := os.ReadFile(a.scriptPath())
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == name {
			return true
		}
	}
	return false
}

// fail carries out a scripted failure. It returns the error to answer with
// (for "error" and "auth"); the other actions do not return.
func (a *agent) fail(step, action, arg string) error {
	fmt.Fprintf(os.Stderr, "fakeagent: scripted %s at %s\n", action, step)
	switch action {
	case "exit":
		code, _ := strconv.Atoi(arg)
		os.Exit(code)
	case "kill":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	case "hang":
		select {}
	case "auth":
		return acp.NewAuthRequired("authenticate first")
	case "slow", "quiet":
		secs, _ := strconv.ParseFloat(arg, 64)
		for end := time.Now().Add(time.Duration(secs * float64(time.Second))); time.Now().Before(end); {
			time.Sleep(100 * time.Millisecond)
			if action == "slow" {
				fmt.Fprintf(os.Stderr, "fakeagent: still working on %s\n", step)
			}
		}
		return nil
	}
	return acp.NewInternalError(map[string]any{"error": "scripted failure at " + step})
}

// failAt runs the script for step, if any.
func (a *agent) failAt(step string) error {
	if action, arg, ok := a.scripted(step); ok {
		return a.fail(step, action, arg)
	}
	return nil
}

func login(dir string) {
	fmt.Println("To sign in, visit https://example.com/device and enter the code FAKE-1234.")
	fmt.Print("Press Enter when done: ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	_ = os.WriteFile(filepath.Join(dir, "credentials"), []byte("fake-subscription-token\n"), 0o600)
	fmt.Println("Logged in.")
}

func (a *agent) loggedIn() bool {
	if !a.flag("require-login") {
		return true
	}
	if os.Getenv("FAKEAGENT_TOKEN") != "" {
		return true
	}
	for _, f := range []string{"credentials", "account.json"} {
		if _, err := os.Stat(filepath.Join(a.dir, f)); err == nil {
			return true
		}
	}
	return false
}

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	if err := a.failAt("initialize"); err != nil {
		return acp.InitializeResponse{}, err
	}
	exe, _ := os.Executable()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "fakeagent", Version: "1.0"},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: !a.flag("no-load"), McpCapabilities: acp.McpCapabilities{Http: true},
			SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}},
		},
		AuthMethods: []acp.AuthMethod{
			{Terminal: &acp.AuthMethodTerminalInline{
				Id: "fake-login", Name: "Fake subscription", Type: "terminal", Args: []string{"login"},
				Description: acp.Ptr("Sign in with a device code"),
				Meta:        map[string]any{"terminal-auth": map[string]any{"command": exe, "args": []string{"login"}, "label": "Fake login"}},
			}},
			{Agent: &acp.AuthMethodAgent{Id: "fake-key", Name: "Fake API key", Description: acp.Ptr("Uses the key in the environment")}},
		},
	}, nil
}

func (a *agent) Authenticate(_ context.Context, p acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if err := a.failAt("authenticate"); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	if p.MethodId != "fake-key" {
		return acp.AuthenticateResponse{}, acp.NewInvalidParams("unknown method")
	}
	a.mu.Lock()
	a.authed = true
	a.mu.Unlock()
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *agent) path(id string) string {
	return filepath.Join(a.dir, "sessions", filepath.Base(id)+".json")
}

func (a *agent) history(id string) []turn {
	var h []turn
	if b, err := os.ReadFile(a.path(id)); err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

func (a *agent) record(id string, t turn) {
	h := append(a.history(id), t)
	b, _ := json.Marshal(h)
	_ = os.WriteFile(a.path(id), b, 0o644)
}

func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mu.Lock()
	authed := a.authed
	a.mu.Unlock()
	if action, arg, ok := a.scripted("session/new"); ok && (action != "auth" || !authed) {
		return acp.NewSessionResponse{}, a.fail("session/new", action, arg)
	}
	if !a.loggedIn() {
		return acp.NewSessionResponse{}, acp.NewAuthRequired("log in first")
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := "fs-" + hex.EncodeToString(b)
	_ = os.WriteFile(a.path(id), []byte("[]"), 0o644)
	a.mu.Lock()
	a.mcp[id] = mcpNames(p.McpServers)
	a.servers[id] = p.McpServers
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: acp.SessionId(id), ConfigOptions: a.configOptions(id)}, nil
}

func (a *agent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := a.failAt("session/load"); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if _, err := os.Stat(a.path(string(p.SessionId))); err != nil {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams("unknown session")
	}
	a.mu.Lock()
	a.mcp[string(p.SessionId)] = mcpNames(p.McpServers)
	a.servers[string(p.SessionId)] = p.McpServers
	a.mu.Unlock()
	for _, t := range a.history(string(p.SessionId)) {
		u := acp.UpdateAgentMessageText(t.Text)
		if t.Role == "user" {
			u = acp.UpdateUserMessageText(t.Text)
		}
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: u})
	}
	return acp.LoadSessionResponse{ConfigOptions: a.configOptions(string(p.SessionId))}, nil
}

func (a *agent) Cancel(_ context.Context, p acp.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.cancels[string(p.SessionId)]; ok {
		c()
	}
	return nil
}

func (a *agent) say(ctx context.Context, sid acp.SessionId, text string) error {
	return a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateAgentMessageText(text)})
}

func (a *agent) Prompt(_ context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	sid := p.SessionId
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[string(sid)] = cancel
	a.mu.Unlock()
	defer cancel()

	var text strings.Builder
	for _, b := range p.Prompt {
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		}
	}
	in := strings.TrimSpace(text.String())
	if f, err := os.OpenFile(filepath.Join(a.dir, "prompts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, strings.ReplaceAll(in, "\n", " "))
		_ = f.Close()
	}
	a.record(string(sid), turn{"user", in})
	fields := strings.Fields(in)
	answer := ""
	if len(fields) > 0 {
		if stop, done, err := a.misbehave(ctx, sid, fields); done {
			return acp.PromptResponse{StopReason: stop}, err
		}
	}
	switch {
	case len(fields) >= 3 && fields[0] == "write":
		path := fields[1]
		content := strings.Join(fields[2:], " ") + "\n"
		old, _ := os.ReadFile(path)
		call := acp.ToolCallId("call-" + strconv.FormatInt(time.Now().UnixNano(), 36))
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.StartToolCall(call, "Write "+path,
			acp.WithStartKind(acp.ToolKindEdit), acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: path}}),
			acp.WithStartContent([]acp.ToolCallContent{acp.ToolDiffContent(path, content, string(old))}))})
		res, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: sid,
			ToolCall:  acp.ToolCallUpdate{ToolCallId: call, Title: acp.Ptr("Write " + path), Kind: acp.Ptr(acp.ToolKindEdit)},
			Options: []acp.PermissionOption{
				{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
				{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
			},
		})
		if err != nil || res.Outcome.Cancelled != nil {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		if res.Outcome.Selected == nil || res.Outcome.Selected.OptionId != "allow" {
			_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(call, acp.WithUpdateStatus(acp.ToolCallStatusFailed))})
			answer = "Okay, I did not change " + path + "."
			break
		}
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(call, acp.WithUpdateStatus(acp.ToolCallStatusFailed))})
			answer = "Writing failed: " + err.Error()
			break
		}
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(call, acp.WithUpdateStatus(acp.ToolCallStatusCompleted))})
		answer = "I wrote " + path + "."
	case len(fields) == 2 && fields[0] == "slow":
		n, _ := strconv.Atoi(fields[1])
		for i := 1; i <= n; i++ {
			select {
			case <-ctx.Done():
				a.record(string(sid), turn{"agent", "(cancelled)"})
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			case <-time.After(200 * time.Millisecond):
			}
			_ = a.say(ctx, sid, fmt.Sprintf("chunk %d ", i))
		}
		answer = "done"
	case len(fields) >= 1 && (fields[0] == "ask" || fields[0] == "send" || fields[0] == "agents"):
		// Agent-to-agent through the platform MCP server, like a real harness would.
		srv := a.platformServer(sid)
		if srv == nil {
			answer = "no agenthold MCP server in this session"
			break
		}
		tool, args := "list_agents", map[string]any{}
		if fields[0] != "agents" {
			if len(fields) < 3 {
				answer = "usage: ask|send <agent> <text>"
				break
			}
			tool = map[string]string{"ask": "ask", "send": "send_message"}[fields[0]]
			args = map[string]any{"agent": fields[1], "text": strings.Join(fields[2:], " ")}
		}
		call := acp.ToolCallId("mcp-" + strconv.FormatInt(time.Now().UnixNano(), 36))
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.StartToolCall(call, "agenthold: "+tool,
			acp.WithStartKind(acp.ToolKindFetch), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(args))})
		out, err := callMCP(srv, tool, args)
		status := acp.ToolCallStatusCompleted
		if err != nil {
			status, out = acp.ToolCallStatusFailed, err.Error()
		}
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(call, acp.WithUpdateStatus(status),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(out))}))})
		answer = tool + " -> " + out
	case len(fields) == 1 && fields[0] == "tools":
		a.mu.Lock()
		servers := a.mcp[string(sid)]
		a.mu.Unlock()
		var skills []string
		if entries, err := os.ReadDir(filepath.Join(a.dir, "skills")); err == nil {
			for _, e := range entries {
				if b, err := os.ReadFile(filepath.Join(a.dir, "skills", e.Name(), "SKILL.md")); err == nil && e.IsDir() {
					skills = append(skills, e.Name()+"("+strconv.Itoa(len(b))+")")
				}
			}
		}
		answer = "mcp=[" + strings.Join(servers, " | ") + "] skills=[" + strings.Join(skills, ",") + "]"
	case len(fields) == 1 && fields[0] == "model":
		answer = a.describeConfig(string(sid))
	case len(fields) == 1 && fields[0] == "ultracode":
		answer = a.describeUltracode(string(sid))
	case len(fields) == 1 && fields[0] == "mode":
		answer = "mode=" + a.config(string(sid)).mode
	case len(fields) == 1 && fields[0] == "whoami":
		account, _ := os.ReadFile(filepath.Join(a.dir, "account.json"))
		answer = "token=" + os.Getenv("FAKEAGENT_TOKEN") + " account=" + strings.TrimSpace(string(account))
	case len(fields) >= 1 && fields[0] == "think":
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateAgentThoughtText("Thinking about " + strings.Join(fields[1:], " "))})
		answer = "Here is my answer."
	default:
		answer = "echo: " + in
	}
	_ = a.say(ctx, sid, answer)
	a.record(string(sid), turn{"agent", answer})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (a *agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

// misbehave runs the failure prompts. done is false for other prompts and
// for failures after which the prompt is answered normally.
func (a *agent) misbehave(ctx context.Context, sid acp.SessionId, f []string) (acp.StopReason, bool, error) {
	arg := ""
	if len(f) > 1 {
		arg = f[1]
	}
	switch f[0] {
	case "exit", "kill":
		_ = a.say(ctx, sid, "about to "+f[0]+" ")
		return "", true, a.fail("prompt", f[0], arg)
	case "hang":
		select {}
	case "error":
		return "", true, a.fail("prompt", "error", "")
	case "garbage":
		_, _ = a.out.Write([]byte("this is not JSON-RPC\n{\"neither\": \"is this\"}\n"))
	case "bigline":
		_, _ = a.out.Write(append(bytes.Repeat([]byte("x"), 11<<20), '\n'))
	case "stderr":
		n, _ := strconv.Atoi(arg)
		_, _ = os.Stderr.Write(append(bytes.Repeat([]byte("e"), n), '\n'))
		for i := range 1000 {
			fmt.Fprintf(os.Stderr, "stderr line %d\n", i)
		}
	case "closestdout":
		_ = os.Stdout.Close()
		select {}
	case "permafter":
		<-ctx.Done() // session/cancel
		res, err := a.conn.RequestPermission(context.Background(), acp.RequestPermissionRequest{
			SessionId: sid,
			ToolCall:  acp.ToolCallUpdate{ToolCallId: "call-after-cancel", Title: acp.Ptr("Late tool call")},
			Options:   []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"}},
		})
		if err != nil || res.Outcome.Cancelled != nil {
			return acp.StopReasonCancelled, true, nil
		}
		_ = a.say(context.Background(), sid, "permission granted after cancel")
		return acp.StopReasonEndTurn, true, nil
	case "spawn":
		helper := exec.CommandContext(context.Background(), "sleep", "300")
		helper.Stdout, helper.Stderr = os.Stdout, os.Stderr
		if arg == "setsid" {
			helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := helper.Start(); err != nil {
			return "", true, err
		}
		_ = os.WriteFile(filepath.Join(a.dir, "helper.pid"), []byte(strconv.Itoa(helper.Process.Pid)), 0o644)
		os.Exit(0)
	}
	return "", false, nil
}
