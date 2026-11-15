// Command fakeagent is a scripted ACP agent for tests and for developing the
// UI without a real model. Prompts:
//
//	write <path> <text...>   edit a file (asks for permission first)
//	slow <n>                 stream n chunks, 200ms apart (cancellable)
//	think <text>             a thought, then an answer
//	whoami                   the account login it was given (env and file)
//	anything else            echo it back
//
// Sessions are stored under $HOME/.fakeagent so session/load replays them
// after a restart. "fakeagent login" is the terminal login method; an
// account login arrives as FAKEAGENT_TOKEN or $HOME/.fakeagent/account.json.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type agent struct {
	conn    *acp.AgentSideConnection
	dir     string
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	mcp     map[string][]string // session -> MCP servers it was given
	servers map[string][]acp.McpServer
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
	a := &agent{dir: dir, cancels: map[string]context.CancelFunc{}, mcp: map[string][]string{}, servers: map[string][]acp.McpServer{}}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	fmt.Fprintln(os.Stderr, "fakeagent ready")
	<-a.conn.Done()
}

func login(dir string) {
	fmt.Println("To sign in, visit https://example.com/device and enter the code FAKE-1234.")
	fmt.Print("Press Enter when done: ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	_ = os.WriteFile(filepath.Join(dir, "credentials"), []byte("fake-subscription-token\n"), 0o600)
	fmt.Println("Logged in.")
}

func (a *agent) loggedIn() bool {
	if os.Getenv("FAKEAGENT_REQUIRE_LOGIN") == "" {
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
	exe, _ := os.Executable()
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "fakeagent", Version: "1.0"},
		AgentCapabilities: acp.AgentCapabilities{LoadSession: os.Getenv("FAKEAGENT_NO_LOAD") == "", McpCapabilities: acp.McpCapabilities{Http: true}},
		AuthMethods: []acp.AuthMethod{{Terminal: &acp.AuthMethodTerminalInline{
			Id: "fake-login", Name: "Fake subscription", Type: "terminal", Args: []string{"login"},
			Description: acp.Ptr("Sign in with a device code"),
			Meta:        map[string]any{"terminal-auth": map[string]any{"command": exe, "args": []string{"login"}, "label": "Fake login"}},
		}}},
	}, nil
}

func (a *agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
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
	return acp.NewSessionResponse{SessionId: acp.SessionId(id)}, nil
}

func (a *agent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
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
	return acp.LoadSessionResponse{}, nil
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
	a.record(string(sid), turn{"user", in})
	fields := strings.Fields(in)
	answer := ""
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

func (a *agent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}
