package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/tunnel"
)

// claudeProtoClient speaks Claude Code's native stream-json control protocol
// directly (`claude -p --input-format stream-json --output-format stream-json
// --verbose --include-partial-messages --dangerously-skip-permissions`),
// matching T3 Code's ClaudeAgentSdkQueryRunner without needing claude-agent-acp.
//
// Because each `claude -p` process serves one conversation thread,
// claudeProtoClient owns an initial control process (used for initialize/model
// discovery and the first fresh session) and spawns additional per-session
// processes over the sandbox tunnel as needed (with `--resume <id>`,
// `--mcp-config <json>`, and the thread's `cwd`).
type claudeProtoClient struct {
	conn *harnessConn
	env  map[string]string
	// spawn opens a new Claude CLI process in dir with extraArgs appended to
	// the adapter's base command. Tests override it to inject in-memory pipes.
	spawn func(ctx context.Context, dir string, extraArgs []string) (io.ReadWriteCloser, error)

	mu       sync.Mutex
	initProc *claudeProc
	procs    map[string]*claudeProc // sessionID -> live Claude process
	cfg      map[string]*claudeSessionCfg
	catalog  []ConfigOption
	byModel  map[string][]ConfigOption
	commands []SlashCommand
	initResp acp.InitializeResponse
	done     chan struct{}
	once     sync.Once
}

type claudeSessionCfg struct {
	model  string
	effort string
	fast   bool
	cwd    string
	mcp    []acp.McpServer
}

type claudeProc struct {
	parent    *claudeProtoClient
	sessionID string // bound native Claude session id (set on init/system)
	rw        io.ReadWriteCloser
	wmu       sync.Mutex
	nextReq   atomic.Int64

	mu      sync.Mutex
	pending map[string]chan claudeControlResponse
	turnCh  chan claudeTurnOutcome
	done    chan struct{}
	once    sync.Once
}

type claudeControlResponse struct {
	Subtype   string          `json:"subtype"` // "success" | "error"
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

type claudeTurnOutcome struct {
	stopReason acp.StopReason
	err        error
}

func newClaudeProtoClient(c *harnessConn, proc *tunnel.Process, env map[string]string) *claudeProtoClient {
	p := &claudeProtoClient{
		conn:  c,
		env:   env,
		procs: map[string]*claudeProc{},
		cfg:   map[string]*claudeSessionCfg{},
		done:  make(chan struct{}),
	}
	p.spawn = func(ctx context.Context, dir string, extraArgs []string) (io.ReadWriteCloser, error) {
		argv := append(slices.Clone(c.adapter.Command), extraArgs...)
		pr, err := c.ts.StartHarnessInDir(ctx, argv, dir, env, func(line string) {
			c.m.lg.Debug("harness stderr", "agent", c.agentID, "line", line)
			c.stderr(line)
		})
		if err != nil {
			return nil, err
		}
		return &procRWC{proc: pr}, nil
	}
	if proc != nil {
		p.initProc = p.attachProc(&procRWC{proc: proc}, "")
	}
	return p
}

func (p *claudeProtoClient) attachProc(rw io.ReadWriteCloser, sessionID string) *claudeProc {
	cp := &claudeProc{
		parent:    p,
		sessionID: sessionID,
		rw:        rw,
		pending:   map[string]chan claudeControlResponse{},
		done:      make(chan struct{}),
	}
	go cp.readLoop()
	return cp
}

func (p *claudeProtoClient) Done() <-chan struct{} { return p.done }

func (p *claudeProtoClient) Close() {
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		var all []*claudeProc
		if p.initProc != nil {
			all = append(all, p.initProc)
			p.initProc = nil
		}
		for sid, cp := range p.procs {
			all = append(all, cp)
			delete(p.procs, sid)
		}
		p.mu.Unlock()
		for _, cp := range all {
			cp.close()
		}
	})
}

func (p *claudeProtoClient) Initialize(ctx context.Context) (acp.InitializeResponse, []ConfigOption, map[string][]ConfigOption, error) {
	p.mu.Lock()
	cp := p.initProc
	p.mu.Unlock()
	if cp == nil {
		rw, err := p.spawn(ctx, "", nil)
		if err != nil {
			return acp.InitializeResponse{}, nil, nil, err
		}
		cp = p.attachProc(rw, "")
		p.mu.Lock()
		p.initProc = cp
		p.mu.Unlock()
	}

	raw, err := cp.controlRequest(ctx, map[string]any{"subtype": "initialize"})
	if err != nil {
		return acp.InitializeResponse{}, nil, nil, err
	}
	var res struct {
		Version  string `json:"claude_code_version"`
		Commands []struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			ArgumentHint string `json:"argumentHint"`
		} `json:"commands"`
		Models []struct {
			Value                 string   `json:"value"`
			DisplayName           string   `json:"displayName"`
			Description           string   `json:"description"`
			SupportsEffort        bool     `json:"supportsEffort"`
			SupportedEffortLevels []string `json:"supportedEffortLevels"`
			SupportsFastMode      bool     `json:"supportsFastMode"`
		} `json:"models"`
	}
	_ = json.Unmarshal(raw, &res)

	var cmds []SlashCommand
	for _, c := range res.Commands {
		if strings.TrimSpace(c.Name) != "" {
			cmds = append(cmds, SlashCommand{Name: c.Name, Description: c.Description, InputHint: c.ArgumentHint})
		}
	}

	mo := ConfigOption{
		ID:       "model",
		Name:     "Model",
		Category: "model",
		Kind:     KindModel,
		Type:     "select",
	}
	byModel := map[string][]ConfigOption{}
	var firstModelOpts []ConfigOption
	for i, m := range res.Models {
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = m.Value
		}
		mo.Values = append(mo.Values, ConfigValue{
			Value:       m.Value,
			Name:        name,
			Description: m.Description,
			Group:       "Claude",
		})
		if i == 0 {
			mo.Current = m.Value
		}
		var mOpts []ConfigOption
		if m.SupportsEffort && len(m.SupportedEffortLevels) > 0 {
			eo := ConfigOption{
				ID:       "effort",
				Name:     "Effort",
				Category: "thought_level",
				Kind:     KindEffort,
				Type:     "select",
				Current:  "medium",
			}
			for _, lvl := range m.SupportedEffortLevels {
				eo.Values = append(eo.Values, ConfigValue{Value: lvl, Name: capitalize(lvl)})
			}
			if !eo.Has(eo.Current) {
				eo.Current = eo.Values[0].Value
			}
			mOpts = append(mOpts, eo)
		}
		if m.SupportsFastMode {
			mOpts = append(mOpts, ConfigOption{
				ID:      "fast",
				Name:    "Fast mode",
				Kind:    KindToggle,
				Type:    "boolean",
				Current: "false",
			})
		}
		byModel[m.Value] = mOpts
		if i == 0 {
			firstModelOpts = mOpts
		}
	}

	var opts []ConfigOption
	if len(mo.Values) > 0 {
		opts = append([]ConfigOption{mo}, firstModelOpts...)
	}

	initResp := acp.InitializeResponse{
		AgentInfo: &acp.Implementation{Name: "claude-code", Version: res.Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				Resume: &acp.SessionResumeCapabilities{},
				Close:  &acp.SessionCloseCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{
			{Terminal: &acp.AuthMethodTerminalInline{
				Id:          "claude-login",
				Name:        "Claude login",
				Description: ptr("Run `claude auth login` inside the sandbox"),
				Args:        []string{"auth", "login"},
			}},
		},
	}

	p.mu.Lock()
	p.catalog = opts
	p.byModel = byModel
	p.commands = cmds
	p.initResp = initResp
	p.mu.Unlock()

	return initResp, opts, byModel, nil
}

func (p *claudeProtoClient) Authenticate(_ context.Context, _ string) error {
	return nil
}

func claudeMCPArgs(mcp []acp.McpServer) []string {
	if len(mcp) == 0 {
		return nil
	}
	servers := map[string]any{}
	for _, s := range mcp {
		switch {
		case s.Stdio != nil:
			entry := map[string]any{
				"command": s.Stdio.Command,
				"args":    s.Stdio.Args,
			}
			if len(s.Stdio.Env) > 0 {
				env := map[string]string{}
				for _, kv := range s.Stdio.Env {
					env[kv.Name] = kv.Value
				}
				entry["env"] = env
			}
			servers[s.Stdio.Name] = entry
		case s.Http != nil:
			entry := map[string]any{
				"type": "http",
				"url":  s.Http.Url,
			}
			if len(s.Http.Headers) > 0 {
				hdrs := map[string]string{}
				for _, h := range s.Http.Headers {
					hdrs[h.Name] = h.Value
				}
				entry["headers"] = hdrs
			}
			servers[s.Http.Name] = entry
		}
	}
	if len(servers) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(raw)}
}

func (p *claudeProtoClient) sessionOptions(sessionID string) []ConfigOption {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.catalog) == 0 {
		return nil
	}
	cfg := p.cfg[sessionID]
	opts := slices.Clone(p.catalog)
	model := opts[0].Current
	if cfg != nil && cfg.model != "" {
		model = cfg.model
		opts[0].Current = model
	}
	if mOpts, ok := p.byModel[model]; ok {
		opts = append([]ConfigOption{opts[0]}, slices.Clone(mOpts)...)
	}
	if cfg != nil {
		for i := range opts {
			if opts[i].Kind == KindEffort && cfg.effort != "" && opts[i].Has(cfg.effort) {
				opts[i].Current = cfg.effort
			}
			if opts[i].ID == "fast" {
				if cfg.fast {
					opts[i].Current = "true"
				} else {
					opts[i].Current = "false"
				}
			}
		}
	}
	return opts
}

func (p *claudeProtoClient) openSessionProc(ctx context.Context, sessionID, resumeID, cwd string, mcp []acp.McpServer) (*claudeProc, error) {
	extra := claudeMCPArgs(mcp)
	if resumeID != "" {
		extra = append(extra, "--resume", resumeID)
	}
	p.mu.Lock()
	cfg := p.cfg[sessionID]
	if cfg != nil && cfg.model != "" {
		extra = append(extra, "--model", cfg.model)
	}
	if cfg != nil && cfg.effort != "" {
		extra = append(extra, "--effort", cfg.effort)
	}
	p.mu.Unlock()

	rw, err := p.spawn(ctx, cwd, extra)
	if err != nil {
		return nil, err
	}
	cp := p.attachProc(rw, sessionID)
	if _, err := cp.controlRequest(ctx, map[string]any{"subtype": "initialize"}); err != nil {
		cp.close()
		return nil, err
	}
	return cp, nil
}

func (p *claudeProtoClient) NewSession(ctx context.Context, cwd string, mcp []acp.McpServer) (string, []ConfigOption, error) {
	sid := id.New("cl")
	cp, err := p.openSessionProc(ctx, sid, "", cwd, mcp)
	if err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	p.procs[sid] = cp
	p.cfg[sid] = &claudeSessionCfg{cwd: cwd, mcp: mcp}
	cmds := slices.Clone(p.commands)
	p.mu.Unlock()
	if p.conn != nil && len(cmds) > 0 {
		p.conn.setCommands(sid, cmds)
	}
	return sid, p.sessionOptions(sid), nil
}

func (p *claudeProtoClient) ResumeSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	cp, err := p.openSessionProc(ctx, sessionID, sessionID, cwd, mcp)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if old := p.procs[sessionID]; old != nil {
		old.close()
	}
	p.procs[sessionID] = cp
	p.cfg[sessionID] = &claudeSessionCfg{cwd: cwd, mcp: mcp}
	cmds := slices.Clone(p.commands)
	p.mu.Unlock()
	if p.conn != nil && len(cmds) > 0 {
		p.conn.setCommands(sessionID, cmds)
	}
	return p.sessionOptions(sessionID), nil
}

func (p *claudeProtoClient) LoadSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	return p.ResumeSession(ctx, sessionID, cwd, mcp)
}

func (p *claudeProtoClient) CloseSession(_ context.Context, sessionID string) error {
	p.mu.Lock()
	cp := p.procs[sessionID]
	delete(p.procs, sessionID)
	delete(p.cfg, sessionID)
	p.mu.Unlock()
	if cp != nil {
		cp.close()
	}
	return nil
}

func (p *claudeProtoClient) SetConfigOption(ctx context.Context, sessionID string, opt ConfigOption, value string) ([]ConfigOption, error) {
	p.mu.Lock()
	cfg := p.cfg[sessionID]
	if cfg == nil {
		cfg = &claudeSessionCfg{}
		p.cfg[sessionID] = cfg
	}
	cp := p.procs[sessionID]
	switch {
	case opt.Kind == KindModel:
		cfg.model = value
	case opt.Kind == KindEffort:
		cfg.effort = value
	case opt.ID == "fast":
		cfg.fast = value == "true" || value == "on"
	}
	p.mu.Unlock()

	if cp != nil && opt.Kind == KindModel {
		if _, err := cp.controlRequest(ctx, map[string]any{"subtype": "set_model", "model": value}); err != nil {
			return nil, err
		}
	}
	return p.sessionOptions(sessionID), nil
}

func (p *claudeProtoClient) InjectHistory(_ context.Context, _, _ string, _ HandoffSelection) (bool, error) {
	return false, nil
}

func (p *claudeProtoClient) Prompt(ctx context.Context, sessionID, text string, images []PromptImage) (acp.PromptResponse, error) {
	p.mu.Lock()
	cp := p.procs[sessionID]
	cfg := p.cfg[sessionID]
	p.mu.Unlock()
	if cp == nil || cp.closed() {
		var cwd string
		var mcp []acp.McpServer
		if cfg != nil {
			cwd, mcp = cfg.cwd, cfg.mcp
		}
		var err error
		cp, err = p.openSessionProc(ctx, sessionID, "", cwd, mcp)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		p.mu.Lock()
		p.procs[sessionID] = cp
		p.mu.Unlock()
	}

	// Ensure Claude stays in full-bypass mode before the prompt (including resumed
	// sessions): the Firecracker microVM is the safety boundary.
	_, _ = cp.controlRequest(ctx, map[string]any{"subtype": "set_permission_mode", "mode": "bypassPermissions"})

	turnCh := make(chan claudeTurnOutcome, 1)
	cp.mu.Lock()
	cp.turnCh = turnCh
	cp.mu.Unlock()
	defer func() {
		cp.mu.Lock()
		if cp.turnCh == turnCh {
			cp.turnCh = nil
		}
		cp.mu.Unlock()
	}()

	var content any = text
	if len(images) > 0 {
		blocks := []map[string]any{{"type": "text", "text": text}}
		for _, im := range images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": im.MimeType, "data": im.Data,
			}})
		}
		content = blocks
	}
	if err := cp.writeLine(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	}); err != nil {
		return acp.PromptResponse{}, err
	}

	select {
	case out := <-turnCh:
		if out.err != nil {
			return acp.PromptResponse{}, out.err
		}
		return acp.PromptResponse{StopReason: out.stopReason}, nil
	case <-ctx.Done():
		return acp.PromptResponse{}, ctx.Err()
	case <-cp.done:
		return acp.PromptResponse{}, errors.New("claude code exited during the turn")
	}
}

func (p *claudeProtoClient) Cancel(ctx context.Context, sessionID string) error {
	p.mu.Lock()
	cp := p.procs[sessionID]
	p.mu.Unlock()
	if cp == nil {
		return nil
	}
	_, err := cp.controlRequest(ctx, map[string]any{"subtype": "interrupt"})
	return err
}

func (cp *claudeProc) closed() bool {
	select {
	case <-cp.done:
		return true
	default:
		return false
	}
}

func (cp *claudeProc) close() {
	cp.once.Do(func() {
		close(cp.done)
		_ = cp.rw.Close()
		cp.mu.Lock()
		for id, ch := range cp.pending {
			ch <- claudeControlResponse{Subtype: "error", Error: "claude code process closed"}
			delete(cp.pending, id)
		}
		if cp.turnCh != nil {
			select {
			case cp.turnCh <- claudeTurnOutcome{err: errors.New("claude code process closed")}:
			default:
			}
		}
		cp.mu.Unlock()
	})
}

func (cp *claudeProc) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	cp.wmu.Lock()
	defer cp.wmu.Unlock()
	_, err = cp.rw.Write(b)
	return err
}

func (cp *claudeProc) controlRequest(ctx context.Context, req map[string]any) (json.RawMessage, error) {
	reqID := fmt.Sprintf("req_%d", cp.nextReq.Add(1))
	ch := make(chan claudeControlResponse, 1)
	cp.mu.Lock()
	if cp.closed() {
		cp.mu.Unlock()
		return nil, errors.New("claude code process closed")
	}
	cp.pending[reqID] = ch
	cp.mu.Unlock()

	if err := cp.writeLine(map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    req,
	}); err != nil {
		cp.mu.Lock()
		delete(cp.pending, reqID)
		cp.mu.Unlock()
		return nil, err
	}

	select {
	case res := <-ch:
		if res.Subtype == "error" {
			return nil, errors.New(res.Error)
		}
		return res.Response, nil
	case <-ctx.Done():
		cp.mu.Lock()
		delete(cp.pending, reqID)
		cp.mu.Unlock()
		return nil, ctx.Err()
	case <-cp.done:
		return nil, errors.New("claude code exited")
	}
}

func (cp *claudeProc) readLoop() {
	defer cp.close()
	br := bufio.NewReaderSize(cp.rw, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if cp.parent.conn != nil {
				cp.parent.conn.alive()
			}
			cp.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (cp *claudeProc) handleLine(line []byte) {
	var env struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		SessionID string          `json:"session_id"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
		Response  json.RawMessage `json:"response"`
		Event     json.RawMessage `json:"event"`
		Message   json.RawMessage `json:"message"`
		IsError   bool            `json:"is_error"`
		Result    string          `json:"result"`
		Errors    []string        `json:"errors"`
		TotalCost float64         `json:"total_cost_usd"`
		Usage     *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			ContextWindow int64 `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(line, &env) != nil {
		return
	}

	// Remember Claude's real session UUID when reported so DB stores it for --resume.
	if env.SessionID != "" && cp.sessionID != "" && env.SessionID != cp.sessionID && cp.parent.conn != nil {
		cp.parent.conn.rebindNativeID(cp.sessionID, env.SessionID)
		cp.parent.mu.Lock()
		if cp.parent.procs[cp.sessionID] == cp {
			delete(cp.parent.procs, cp.sessionID)
			cp.parent.procs[env.SessionID] = cp
		}
		if cfg := cp.parent.cfg[cp.sessionID]; cfg != nil {
			delete(cp.parent.cfg, cp.sessionID)
			cp.parent.cfg[env.SessionID] = cfg
		}
		cp.parent.mu.Unlock()
		cp.sessionID = env.SessionID
	}

	switch env.Type {
	case "control_response":
		var resp claudeControlResponse
		if json.Unmarshal(env.Response, &resp) == nil && resp.RequestID != "" {
			cp.mu.Lock()
			ch := cp.pending[resp.RequestID]
			delete(cp.pending, resp.RequestID)
			cp.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
		}
	case "control_request":
		go cp.handleControlRequest(env.RequestID, env.Request)
	case "stream_event":
		cp.handleStreamEvent(env.Event)
	case "assistant":
		cp.handleAssistantMessage(env.Message)
	case "user":
		cp.handleToolResultMessage(env.Message)
	case "result":
		// Emit context window / cost usage update before ending the turn.
		if env.Usage != nil {
			used := env.Usage.InputTokens + env.Usage.CacheCreationInputTokens + env.Usage.CacheReadInputTokens + env.Usage.OutputTokens
			var maxWin int64
			for _, mu := range env.ModelUsage {
				if mu.ContextWindow > maxWin {
					maxWin = mu.ContextWindow
				}
			}
			if used > 0 {
				cp.emitUpdate(map[string]any{
					"sessionUpdate": "usage_update",
					"used":          used,
					"size":          maxWin,
					"cost":          map[string]any{"amount": env.TotalCost, "currency": "USD"},
				})
			}
		}
		var out claudeTurnOutcome
		switch {
		case env.IsError || env.Subtype == "error_during_execution":
			msg := env.Result
			if msg == "" && len(env.Errors) > 0 {
				msg = strings.Join(env.Errors, "; ")
			}
			if msg == "" {
				msg = "Claude Code turn failed"
			}
			out.err = errors.New(msg)
		case env.Subtype == "interrupted":
			out.stopReason = acp.StopReasonCancelled
		default:
			out.stopReason = acp.StopReasonEndTurn
		}
		cp.mu.Lock()
		ch := cp.turnCh
		cp.mu.Unlock()
		if ch != nil {
			select {
			case ch <- out:
			default:
			}
		}
	}
}

func (cp *claudeProc) handleStreamEvent(raw json.RawMessage) {
	var ev struct {
		Type  string `json:"type"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "content_block_delta" {
		return
	}
	switch ev.Delta.Type {
	case "text_delta":
		if ev.Delta.Text != "" {
			cp.emitUpdate(map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": ev.Delta.Text},
			})
		}
	case "thinking_delta":
		if ev.Delta.Thinking != "" {
			cp.emitUpdate(map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"type": "text", "text": ev.Delta.Thinking},
			})
		}
	}
}

func (cp *claudeProc) handleAssistantMessage(raw json.RawMessage) {
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	for _, block := range msg.Content {
		if block.Type == "tool_use" && block.ID != "" {
			cp.emitUpdate(map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    block.ID,
				"title":         block.Name,
				"kind":          claudeToolKind(block.Name),
				"status":        "in_progress",
				"rawInput":      block.Input,
			})
		}
	}
}

func (cp *claudeProc) handleToolResultMessage(raw json.RawMessage) {
	var msg struct {
		Content []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	for _, block := range msg.Content {
		if block.Type == "tool_result" && block.ToolUseID != "" {
			status := "completed"
			if block.IsError {
				status = "failed"
			}
			cp.emitUpdate(map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    block.ToolUseID,
				"status":        status,
				"rawOutput":     block.Content,
			})
		}
	}
}

func (cp *claudeProc) handleControlRequest(reqID string, raw json.RawMessage) {
	var req struct {
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		Input     json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	reply := func(payload map[string]any) {
		_ = cp.writeLine(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": reqID,
				"response":   payload,
			},
		})
	}

	if req.Subtype != "can_use_tool" {
		reply(map[string]any{})
		return
	}

	// The Firecracker microVM is the safety boundary: always allow tool calls
	// without prompting if Claude ever sends a can_use_tool request.
	var inputObj any
	_ = json.Unmarshal(req.Input, &inputObj)
	if inputObj == nil {
		inputObj = map[string]any{}
	}
	reply(map[string]any{"behavior": "allow", "updatedInput": inputObj})
}

func (cp *claudeProc) emitUpdate(update map[string]any) {
	if cp.parent.conn == nil {
		return
	}
	raw, err := json.Marshal(update)
	if err != nil {
		return
	}
	cp.parent.conn.emitRawUpdate(cp.sessionID, raw)
}

func claudeToolKind(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell":
		return "execute"
	case "read":
		return "read"
	case "edit", "write", "multiedit", "notebookedit":
		return "edit"
	case "grep", "glob":
		return "search"
	case "webfetch", "websearch":
		return "fetch"
	default:
		return "other"
	}
}

// Keep unused harness import referenced for compile hygiene when needed.
var _ = harness.ProtocolClaude
