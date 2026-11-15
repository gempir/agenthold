// Package platform serves the agenthold MCP tools that agents use from inside
// their sandboxes: list the agents they may talk to, send them messages, and
// ask them for an answer. An agent may talk to the agents its owner owns or
// that are shared with its owner. Calls arrive over the caller's tunnel, so the server
// knows which agent is calling; it never trusts the sandbox for that.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
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

// Handle serves one call from the agent behind s.
func (p *Service) Handle(ctx context.Context, s *tunnel.Session, st net.Conn, o tunnel.Open) {
	res, err := p.Call(ctx, s.AgentID, o.Session, o.Tool, o.Args)
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
		return Tools, nil
	}
	caller, err := p.Store.GetAgent(ctx, callerID)
	if err != nil {
		return nil, err
	}
	var in struct {
		Agent string `json:"agent"`
		Text  string `json:"text"`
	}
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
		if r.Agent.ID == caller.ID {
			continue
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
