// Package platform serves the agenthold MCP tools that agents use from inside
// their sandboxes: list the agents they may talk to, send them messages, ask
// them for an answer, expose ports and run services on their own machine,
// and deploy and manage apps. An agent may talk to the agents its owner owns or
// that are shared with its owner, and manage its owner's apps (change and
// delete only the ones it created). Calls arrive over the caller's tunnel, so the server
// knows which agent is calling; it never trusts the sandbox for that.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// MaxDepth bounds chains of agents prompting agents (A asks B asks C ...).
const MaxDepth = 4

// ServerName is the MCP server name harnesses see.
const ServerName = "agenthold"

// Service handles platform tool calls.
type Service struct {
	Store    *store.Store
	Sessions *session.Manager
	Audit    *audit.Log
	Log      *slog.Logger
	// AskTimeout bounds ask(). Default 30 minutes.
	AskTimeout time.Duration
	// Preview serves exposures; the exposure tools are listed when it is enabled.
	Preview *preview.Service
	// Apps runs services and apps; their tools are listed when it is set.
	Apps *apps.Manager
}

// Tools are the platform tools in MCP form.
var Tools = []map[string]any{
	{
		"name":        "list_agents",
		"description": "List the other agents you can message (your owner's agents and agents shared with your owner), with their state.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "send_message",
		"description": "Send a message to another agent and return immediately. The other agent works on it " +
			"in its own session; use ask instead if you need its answer.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"agent", "text"},
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "The agent's name or id (see list_agents)."},
				"text":  map[string]any{"type": "string", "description": "The message."},
			},
		},
	},
	{
		"name": "ask",
		"description": "Ask another agent to do something and wait for its final answer. This can take minutes " +
			"while the other agent works. It wakes the agent if it sleeps.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"agent", "text"},
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "The agent's name or id (see list_agents)."},
				"text":  map[string]any{"type": "string", "description": "What you want from it."},
			},
		},
	},
}

// exposureTools act on the calling agent's own machine.
var exposureTools = []map[string]any{
	{
		"name": "expose_port",
		"description": "Serve a TCP port of your machine over HTTPS at a preview URL and return the URL. " +
			"Start the server first (it may listen on 127.0.0.1). Access \"private\" (default) lets in your owner " +
			"and the people your machine is shared with, \"authenticated\" anyone signed in to this agenthold. " +
			"Only your owner can make an exposure public. Signed-in visitors' requests carry X-Agenthold-User, " +
			"X-Agenthold-Email and a signed X-Agenthold-Identity JWT.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"port"},
			"properties": map[string]any{
				"port":   map[string]any{"type": "integer", "description": "The port your server listens on."},
				"name":   map[string]any{"type": "string", "description": "Optional host name: lowercase letters, digits and hyphens, up to 40."},
				"access": map[string]any{"type": "string", "enum": []string{"private", "authenticated"}},
			},
		},
	},
	{
		"name":        "list_exposures",
		"description": "List the ports of your machine that are exposed, with their URLs and access.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "unexpose",
		"description": "Stop serving a port of your machine at its preview URL.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"port"},
			"properties": map[string]any{"port": map[string]any{"type": "integer"}},
		},
	},
}

// tools lists the tools offered to agents.
func (p *Service) tools() []map[string]any {
	out := append([]map[string]any{}, Tools...)
	if p.Preview.Enabled() {
		out = append(out, exposureTools...)
	}
	if p.Apps != nil {
		out = append(append(out, serviceTools...), appTools...)
	}
	return out
}

// Handle serves one call from the agent behind s.
func (p *Service) Handle(ctx context.Context, agentID string, st io.ReadWriteCloser, o tunnel.Open) {
	res, err := p.Call(ctx, agentID, o.Session, o.Tool, o.Args)
	if err != nil {
		_ = tunnel.WriteError(st, err)
		return
	}
	_ = tunnel.WriteJSON(st, res)
}

// Errors returned to the calling agent.
var (
	ErrDepth   = fmt.Errorf("too many agents in a row (limit %d): this chain of agent messages stops here", MaxDepth)
	ErrNoAgent = errors.New("no agent with that name or id that you can message")
	ErrSelf    = errors.New("an agent cannot message itself")
)

// Call runs a tool for callerID. sessionID is the caller's session (for linking).
func (p *Service) Call(ctx context.Context, callerID, sessionID, tool string, args json.RawMessage) (any, error) {
	if tool == "_list_tools" {
		return p.tools(), nil
	}
	caller, err := p.Store.GetAgent(ctx, callerID)
	if err != nil {
		return nil, err
	}
	if caller.Kind == apps.KindApp {
		// Only agents act on the platform; an app's processes get nothing.
		return nil, errors.New("platform tools are for agents, not apps")
	}
	var in toolArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	switch tool {
	case "list_agents":
		return p.listAgents(ctx, caller)
	case "send_message", "ask":
		if strings.TrimSpace(in.Text) == "" {
			return nil, errors.New("text is required")
		}
		return p.message(ctx, caller, sessionID, in.Agent, in.Text, tool == "ask")
	case "expose_port":
		return p.expose(ctx, caller, in.Port, in.Name, preview.Access(in.Access))
	case "list_exposures":
		return p.listExposures(ctx, caller)
	case "unexpose":
		return p.unexpose(ctx, caller, in.Port)
	}
	if p.Apps == nil {
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
	switch tool {
	case "start_service":
		return p.startService(ctx, caller, in)
	case "stop_service":
		return p.stopService(ctx, caller, in.Name)
	case "list_services":
		return p.listServices(ctx, caller)
	case "service_logs":
		return p.serviceLogs(ctx, caller.ID, in.Name, in.Lines)
	case "deploy_app":
		dctx, cancel := context.WithTimeout(ctx, apps.DeployTimeout+time.Minute)
		defer cancel()
		return p.deployApp(dctx, caller, in)
	case "list_apps":
		return p.listApps(ctx, caller)
	case "app_logs":
		app, err := p.findApp(ctx, caller, in.Name)
		if err != nil {
			return nil, err
		}
		return p.serviceLogs(ctx, app.ID, apps.AppService, in.Lines)
	case "app_exec":
		return p.appExec(ctx, caller, in)
	case "restart_app":
		return p.restartApp(ctx, caller, in.Name)
	case "stop_app":
		return p.stopApp(ctx, caller, in.Name)
	case "delete_app":
		return p.deleteApp(ctx, caller, in.Name)
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

type agentInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness"`
	State   string `json:"state"`
}

// reachable lists the agents caller may message: those its owner owns or
// that are shared with its owner, except caller itself.
func (p *Service) reachable(ctx context.Context, caller db.Agent) ([]db.ListUserAgentsRow, error) {
	rows, err := p.Store.ListUserAgents(ctx, caller.OwnerID)
	if err != nil {
		return nil, err
	}
	chk := authz.Checker{Q: p.Store.Queries}
	var out []db.ListUserAgentsRow
	for _, r := range rows {
		if r.Agent.ID == caller.ID || r.Agent.Kind == apps.KindApp || r.Agent.IsTemplate == 1 {
			continue // apps run no harness; templates are never running: nothing to message
		}
		if ok, err := chk.CanDelegate(ctx, caller.OwnerID, r.Agent); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (p *Service) listAgents(ctx context.Context, caller db.Agent) (string, error) {
	rows, err := p.reachable(ctx, caller)
	if err != nil {
		return "", err
	}
	var out []agentInfo
	for _, r := range rows {
		out = append(out, agentInfo{ID: r.Agent.ID, Name: r.Agent.Name, Harness: r.Agent.Harness, State: r.Sandbox.State})
	}
	if len(out) == 0 {
		return "There are no other agents you can message.", nil
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

// resolve finds the target by id or name among the agents caller may
// message. Agents it may not message are indistinguishable from missing ones.
func (p *Service) resolve(ctx context.Context, caller db.Agent, ref string) (db.Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == caller.ID || strings.EqualFold(ref, caller.Name) {
		return caller, nil
	}
	rows, err := p.reachable(ctx, caller)
	if err != nil {
		return db.Agent{}, err
	}
	var found []db.Agent
	for _, r := range rows {
		if r.Agent.ID == ref {
			return r.Agent, nil
		}
		if strings.EqualFold(r.Agent.Name, ref) {
			found = append(found, r.Agent)
		}
	}
	switch len(found) {
	case 0:
		return db.Agent{}, ErrNoAgent
	case 1:
		return found[0], nil
	}
	return db.Agent{}, fmt.Errorf("several agents are called %q; use its id", ref)
}

func (p *Service) message(ctx context.Context, caller db.Agent, callerSession, ref, text string, wait bool) (string, error) {
	target, err := p.resolve(ctx, caller, ref)
	if err != nil {
		return "", err
	}
	if target.ID == caller.ID {
		return "", ErrSelf
	}
	// The depth comes from the server's view of the caller's running turns,
	// never from the sandbox.
	depth := p.Sessions.AgentDepth(caller.ID) + 1
	if depth > MaxDepth {
		p.logger().Warn("agent chain stopped by the depth limit", "caller", caller.ID, "target", target.ID, "depth", depth)
		return "", ErrDepth
	}
	sessionID, err := p.linkedSession(ctx, caller, callerSession, target)
	if err != nil {
		return "", err
	}
	owner, err := p.Store.GetUser(ctx, caller.OwnerID)
	if err != nil {
		return "", err
	}
	opts := session.PromptOpts{Depth: depth, Origin: map[string]string{"agentId": caller.ID, "agentName": caller.Name, "sessionId": callerSession}}
	name := "agent " + caller.Name + " (for " + owner.Name + ")"
	p.Audit.Record(ctx, audit.Entry{
		AgentID: target.ID, UserID: caller.OwnerID, Action: audit.Prompt, Target: sessionID,
		Details: map[string]any{"text": truncate(text, 500), "fromAgent": caller.Name, "fromAgentId": caller.ID, "depth": depth, "wait": wait},
	})
	if !wait {
		if err := p.Sessions.PromptWith(ctx, sessionID, caller.OwnerID, name, text, nil, opts); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent to %s (session %s). It works on it on its own.", target.Name, sessionID), nil
	}
	timeout := p.AskTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := p.Sessions.Ask(actx, sessionID, caller.OwnerID, name, text, opts)
	if err != nil {
		return "", fmt.Errorf("%s could not answer: %w", target.Name, err)
	}
	if answer == "" {
		answer = "(no answer text)"
	}
	return answer, nil
}

// linkedSession reuses the caller's idle conversation with the target, or starts one.
func (p *Service) linkedSession(ctx context.Context, caller db.Agent, callerSession string, target db.Agent) (string, error) {
	rows, err := p.Store.LinkedSessions(ctx, db.LinkedSessionsParams{AgentID: target.ID, OriginAgentID: caller.ID})
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if !p.Sessions.Running(r.ID) {
			return r.ID, nil
		}
	}
	s, err := p.Sessions.Create(ctx, target.ID, caller.OwnerID, "From "+caller.Name)
	if err != nil {
		return "", err
	}
	if err := p.Store.SetSessionOrigin(ctx, db.SetSessionOriginParams{OriginAgentID: caller.ID, OriginSessionID: callerSession, ID: s.ID}); err != nil {
		return "", err
	}
	return s.ID, nil
}

func (p *Service) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
