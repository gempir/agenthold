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

	"github.com/gempir/agenthold/internal/tunnel"
	"github.com/gempir/agenthold/internal/version"
)

// codexProtoClient speaks OpenAI Codex's native JSON-RPC 2.0 app-server
// protocol (`codex app-server`) directly over NDJSON stdio, matching T3 Code's
// effect-codex-app-server + CodexAdapterV2.
type codexProtoClient struct {
	conn *harnessConn
	rw   io.ReadWriteCloser

	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan jsonRPCMessage
	turns   map[string]*codexActiveTurn // threadId -> active turn
	state   map[string]*codexThreadCfg  // threadId -> chosen model/effort/fast
	catalog []ConfigOption
	byModel map[string][]ConfigOption
	done    chan struct{}
	once    sync.Once
}

type codexThreadCfg struct {
	model   string
	effort  string
	fast    bool
	cwd     string
	turnID  string
	toolMap map[string]string // itemId -> title/kind
}

type codexActiveTurn struct {
	turnID string
	done   chan codexTurnResult
}

type codexTurnResult struct {
	stopReason acp.StopReason
	err        error
}

type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func newCodexProtoClient(c *harnessConn, proc *tunnel.Process) *codexProtoClient {
	return newCodexProtoClientRW(c, &procRWC{proc: proc})
}

type procRWC struct{ proc *tunnel.Process }

func (p *procRWC) Read(b []byte) (int, error)  { return p.proc.Stdout.Read(b) }
func (p *procRWC) Write(b []byte) (int, error) { return p.proc.Write(b) }
func (p *procRWC) Close() error                { return p.proc.Close() }

func newCodexProtoClientRW(c *harnessConn, rw io.ReadWriteCloser) *codexProtoClient {
	p := &codexProtoClient{
		conn:    c,
		rw:      rw,
		pending: map[int64]chan jsonRPCMessage{},
		turns:   map[string]*codexActiveTurn{},
		state:   map[string]*codexThreadCfg{},
		done:    make(chan struct{}),
	}
	go p.readLoop()
	return p
}

func (p *codexProtoClient) Done() <-chan struct{} { return p.done }
func (p *codexProtoClient) Close() {
	p.once.Do(func() {
		close(p.done)
		p.failPending(errors.New("codex app-server connection closed"))
	})
}

func (p *codexProtoClient) failPending(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ch := range p.pending {
		ch <- jsonRPCMessage{Error: &jsonRPCError{Code: -32001, Message: err.Error()}}
		delete(p.pending, id)
	}
	for tid, t := range p.turns {
		select {
		case t.done <- codexTurnResult{err: err}:
		default:
		}
		delete(p.turns, tid)
	}
}

func (p *codexProtoClient) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.rw.Write(b)
	return err
}

func (p *codexProtoClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	ch := make(chan jsonRPCMessage, 1)
	p.mu.Lock()
	select {
	case <-p.done:
		p.mu.Unlock()
		return nil, errors.New("codex app-server connection closed")
	default:
	}
	p.pending[id] = ch
	p.mu.Unlock()

	idRaw, _ := json.Marshal(id)
	var paramsRaw json.RawMessage
	if params != nil {
		var err error
		paramsRaw, err = json.Marshal(params)
		if err != nil {
			p.mu.Lock()
			delete(p.pending, id)
			p.mu.Unlock()
			return nil, err
		}
	}
	if err := p.writeLine(jsonRPCMessage{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, &acp.RequestError{Code: msg.Error.Code, Message: msg.Error.Message, Data: msg.Error.Data}
		}
		return msg.Result, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("codex app-server exited")
	}
}

func (p *codexProtoClient) notify(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		var err error
		paramsRaw, err = json.Marshal(params)
		if err != nil {
			return err
		}
	}
	return p.writeLine(jsonRPCMessage{JSONRPC: "2.0", Method: method, Params: paramsRaw})
}

func (p *codexProtoClient) readLoop() {
	defer p.Close()
	br := bufio.NewReaderSize(p.rw, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if p.conn != nil {
				p.conn.alive()
			}
			var msg jsonRPCMessage
			if json.Unmarshal(line, &msg) == nil {
				p.dispatch(msg)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *codexProtoClient) dispatch(msg jsonRPCMessage) {
	// Response to one of our outgoing requests.
	if msg.Method == "" && len(msg.ID) > 0 {
		var id int64
		if json.Unmarshal(msg.ID, &id) == nil {
			p.mu.Lock()
			ch := p.pending[id]
			delete(p.pending, id)
			p.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
		return
	}
	// Server-initiated request (approvals / user input).
	if msg.Method != "" && len(msg.ID) > 0 {
		go p.handleServerRequest(msg)
		return
	}
	// Server notification.
	if msg.Method != "" {
		p.handleNotification(msg.Method, msg.Params)
	}
}

func (p *codexProtoClient) Initialize(ctx context.Context) (acp.InitializeResponse, []ConfigOption, map[string][]ConfigOption, error) {
	raw, err := p.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "agenthold",
			"title":   "agenthold",
			"version": version.Version,
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	})
	if err != nil {
		return acp.InitializeResponse{}, nil, nil, err
	}
	_ = p.notify("initialized", nil)

	var initRes struct {
		UserAgent string `json:"userAgent"`
		Version   string `json:"version"`
	}
	_ = json.Unmarshal(raw, &initRes)

	opts, byModel := p.fetchModels(ctx)
	p.mu.Lock()
	p.catalog = opts
	p.byModel = byModel
	p.mu.Unlock()

	ver := initRes.Version
	if ver == "" {
		ver = initRes.UserAgent
	}
	resp := acp.InitializeResponse{
		AgentInfo: &acp.Implementation{Name: "codex-app-server", Version: ver},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				Resume: &acp.SessionResumeCapabilities{},
				Close:  &acp.SessionCloseCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{
			{Terminal: &acp.AuthMethodTerminalInline{
				Id:          "chatgpt-device",
				Name:        "ChatGPT device login",
				Description: ptr("Run `codex login --device-auth` inside the sandbox"),
				Args:        []string{"login", "--device-auth"},
			}},
		},
	}
	return resp, opts, byModel, nil
}

type codexModelEntry struct {
	Model                     string `json:"model"`
	DisplayName               string `json:"displayName"`
	Description               string `json:"description"`
	IsDefault                 bool   `json:"isDefault"`
	DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
}

func (p *codexProtoClient) fetchModels(ctx context.Context) ([]ConfigOption, map[string][]ConfigOption) {
	var all []codexModelEntry
	cursor := ""
	for range 10 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := p.request(ctx, "model/list", params)
		if err != nil {
			break
		}
		var page struct {
			Data       []codexModelEntry `json:"data"`
			NextCursor string            `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			break
		}
		all = append(all, page.Data...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(all) == 0 {
		return nil, nil
	}
	mo := ConfigOption{
		ID:       "model",
		Name:     "Model",
		Category: "model",
		Kind:     KindModel,
		Type:     "select",
	}
	byModel := map[string][]ConfigOption{}
	var defaultModelOpts []ConfigOption
	for _, m := range all {
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = m.Model
		}
		mo.Values = append(mo.Values, ConfigValue{
			Value:       m.Model,
			Name:        name,
			Description: m.Description,
			Group:       "OpenAI",
		})
		if m.IsDefault || mo.Current == "" {
			mo.Current = m.Model
		}
		var mOpts []ConfigOption
		if len(m.SupportedReasoningEfforts) > 0 {
			eo := ConfigOption{
				ID:       "reasoning_effort",
				Name:     "Reasoning effort",
				Category: "thought_level",
				Kind:     KindEffort,
				Type:     "select",
				Current:  m.DefaultReasoningEffort,
			}
			for _, e := range m.SupportedReasoningEfforts {
				eo.Values = append(eo.Values, ConfigValue{
					Value:       e.ReasoningEffort,
					Name:        capitalize(e.ReasoningEffort),
					Description: e.Description,
				})
				if eo.Current == "" {
					eo.Current = e.ReasoningEffort
				}
			}
			mOpts = append(mOpts, eo)
		}
		mOpts = append(mOpts, ConfigOption{
			ID:      "fast-mode",
			Name:    "Fast mode",
			Kind:    KindToggle,
			Type:    "boolean",
			Current: "false",
		})
		byModel[m.Model] = mOpts
		if m.Model == mo.Current {
			defaultModelOpts = mOpts
		}
	}
	opts := append([]ConfigOption{mo}, defaultModelOpts...)
	return opts, byModel
}

func (p *codexProtoClient) Authenticate(_ context.Context, _ string) error {
	// Codex app-server reads ~/.codex/auth.json or OPENAI_API_KEY directly.
	return nil
}

func codexMCPConfig(mcp []acp.McpServer) map[string]any {
	cfg := map[string]any{}
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
			cfg["mcp_servers."+s.Stdio.Name] = entry
		case s.Http != nil:
			entry := map[string]any{
				"url": s.Http.Url,
			}
			if len(s.Http.Headers) > 0 {
				hdrs := map[string]string{}
				for _, h := range s.Http.Headers {
					hdrs[h.Name] = h.Value
				}
				entry["http_headers"] = hdrs
			}
			cfg["mcp_servers."+s.Http.Name] = entry
		}
	}
	return cfg
}

func (p *codexProtoClient) threadOptions(threadID string) []ConfigOption {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.catalog) == 0 {
		return nil
	}
	st := p.state[threadID]
	opts := slices.Clone(p.catalog)
	model := opts[0].Current
	if st != nil && st.model != "" {
		model = st.model
		opts[0].Current = model
	}
	if mOpts, ok := p.byModel[model]; ok {
		opts = append([]ConfigOption{opts[0]}, slices.Clone(mOpts)...)
	}
	if st != nil {
		for i := range opts {
			if opts[i].Kind == KindEffort && st.effort != "" && opts[i].Has(st.effort) {
				opts[i].Current = st.effort
			}
			if opts[i].ID == "fast-mode" {
				if st.fast {
					opts[i].Current = "true"
				} else {
					opts[i].Current = "false"
				}
			}
		}
	}
	return opts
}

func (p *codexProtoClient) NewSession(ctx context.Context, cwd string, mcp []acp.McpServer) (string, []ConfigOption, error) {
	params := map[string]any{
		"cwd":            cwd,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
		"config":         codexMCPConfig(mcp),
	}
	raw, err := p.request(ctx, "thread/start", params)
	if err != nil {
		return "", nil, err
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
		return "", nil, fmt.Errorf("codex thread/start returned no thread id")
	}
	p.mu.Lock()
	p.state[res.Thread.ID] = &codexThreadCfg{cwd: cwd, toolMap: map[string]string{}}
	p.mu.Unlock()
	return res.Thread.ID, p.threadOptions(res.Thread.ID), nil
}

func (p *codexProtoClient) ResumeSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	params := map[string]any{
		"threadId":       sessionID,
		"excludeTurns":   true,
		"cwd":            cwd,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
		"config":         codexMCPConfig(mcp),
	}
	_, err := p.request(ctx, "thread/resume", params)
	if err != nil {
		// Unarchive if archived, matching T3 Code's CodexAdapterV2.
		if strings.Contains(strings.ToLower(err.Error()), "archived") {
			if _, uerr := p.request(ctx, "thread/unarchive", map[string]any{"threadId": sessionID}); uerr == nil {
				_, err = p.request(ctx, "thread/resume", params)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.state[sessionID] = &codexThreadCfg{cwd: cwd, toolMap: map[string]string{}}
	p.mu.Unlock()
	return p.threadOptions(sessionID), nil
}

func (p *codexProtoClient) LoadSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	return p.ResumeSession(ctx, sessionID, cwd, mcp)
}

func (p *codexProtoClient) CloseSession(ctx context.Context, sessionID string) error {
	_, err := p.request(ctx, "thread/unsubscribe", map[string]any{"threadId": sessionID})
	p.mu.Lock()
	delete(p.state, sessionID)
	p.mu.Unlock()
	return err
}

func (p *codexProtoClient) SetConfigOption(_ context.Context, sessionID string, opt ConfigOption, value string) ([]ConfigOption, error) {
	p.mu.Lock()
	st := p.state[sessionID]
	if st == nil {
		st = &codexThreadCfg{toolMap: map[string]string{}}
		p.state[sessionID] = st
	}
	switch {
	case opt.Kind == KindModel:
		st.model = value
	case opt.Kind == KindEffort:
		st.effort = value
	case opt.ID == "fast-mode":
		st.fast = value == "true" || value == "on"
	}
	p.mu.Unlock()
	return p.threadOptions(sessionID), nil
}

// InjectHistory sends historical messages natively via Codex App Server's
// `thread/inject_items` RPC (matching T3 Code's CodexAdapterV2.injectHistory).
// Returns (false, nil) if the installed Codex version lacks the method so
// the caller falls back seamlessly to inline context handoff.
func (p *codexProtoClient) InjectHistory(ctx context.Context, sessionID, threadID string, sel HandoffSelection) (bool, error) {
	items := HistoryResponseItems(threadID, sel)
	_, err := p.request(ctx, "thread/inject_items", map[string]any{
		"threadId": sessionID,
		"items":    items,
	})
	if err != nil {
		var re *acp.RequestError
		if errors.As(err, &re) && re.Code == -32601 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (p *codexProtoClient) Prompt(ctx context.Context, sessionID, text string, images []PromptImage) (acp.PromptResponse, error) {
	p.mu.Lock()
	st := p.state[sessionID]
	var model, effort string
	var fast bool
	if st != nil {
		model, effort, fast = st.model, st.effort, st.fast
	}
	active := &codexActiveTurn{done: make(chan codexTurnResult, 1)}
	p.turns[sessionID] = active
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if p.turns[sessionID] == active {
			delete(p.turns, sessionID)
		}
		p.mu.Unlock()
	}()

	// The Firecracker microVM is the safety boundary: never prompt for approval
	// and disable Codex's inner sandbox.
	input := []map[string]any{{"type": "text", "text": text}}
	for _, im := range images {
		input = append(input, map[string]any{"type": "image", "url": "data:" + im.MimeType + ";base64," + im.Data})
	}
	params := map[string]any{
		"threadId":       sessionID,
		"input":          input,
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]any{"type": "dangerFullAccess"},
	}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	if fast {
		params["serviceTier"] = "fast"
	}

	raw, err := p.request(ctx, "turn/start", params)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &started) == nil && started.Turn.ID != "" {
		p.mu.Lock()
		active.turnID = started.Turn.ID
		if p.state[sessionID] != nil {
			p.state[sessionID].turnID = started.Turn.ID
		}
		p.mu.Unlock()
	}

	select {
	case res := <-active.done:
		if res.err != nil {
			return acp.PromptResponse{}, res.err
		}
		return acp.PromptResponse{StopReason: res.stopReason}, nil
	case <-ctx.Done():
		return acp.PromptResponse{}, ctx.Err()
	case <-p.done:
		return acp.PromptResponse{}, errors.New("codex app-server exited during turn")
	}
}

func (p *codexProtoClient) Cancel(ctx context.Context, sessionID string) error {
	p.mu.Lock()
	var turnID string
	if act := p.turns[sessionID]; act != nil {
		turnID = act.turnID
	}
	p.mu.Unlock()
	if turnID == "" {
		return nil
	}
	_, err := p.request(ctx, "turn/interrupt", map[string]any{
		"threadId": sessionID,
		"turnId":   turnID,
	})
	return err
}

func (p *codexProtoClient) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/agentMessage/delta":
		var n struct {
			ThreadID string `json:"threadId"`
			Delta    string `json:"delta"`
		}
		if json.Unmarshal(params, &n) == nil && n.Delta != "" {
			p.emitTextChunk(n.ThreadID, "agent_message_chunk", n.Delta)
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var n struct {
			ThreadID string `json:"threadId"`
			Delta    string `json:"delta"`
		}
		if json.Unmarshal(params, &n) == nil && n.Delta != "" {
			p.emitTextChunk(n.ThreadID, "agent_thought_chunk", n.Delta)
		}
	case "item/commandExecution/outputDelta":
		var n struct {
			ThreadID string `json:"threadId"`
			ItemID   string `json:"itemId"`
			Delta    string `json:"delta"`
		}
		if json.Unmarshal(params, &n) == nil && n.ItemID != "" && n.Delta != "" {
			p.emitRawUpdate(n.ThreadID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    n.ItemID,
				"status":        "in_progress",
				"rawOutput":     n.Delta,
			})
		}
	case "item/started":
		var n struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Command string `json:"command"`
				Tool    string `json:"tool"`
				Query   string `json:"query"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &n) == nil && n.Item.ID != "" {
			switch n.Item.Type {
			case "commandExecution":
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    n.Item.ID,
					"title":         n.Item.Command,
					"kind":          "execute",
					"status":        "in_progress",
					"rawInput":      map[string]any{"command": n.Item.Command},
				})
			case "fileChange":
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    n.Item.ID,
					"title":         "Edit files",
					"kind":          "edit",
					"status":        "in_progress",
				})
			case "mcpToolCall", "dynamicToolCall":
				title := n.Item.Tool
				if title == "" {
					title = n.Item.Type
				}
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    n.Item.ID,
					"title":         title,
					"kind":          "other",
					"status":        "in_progress",
				})
			case "webSearch":
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    n.Item.ID,
					"title":         "Search: " + n.Item.Query,
					"kind":          "fetch",
					"status":        "in_progress",
				})
			}
		}
	case "item/completed":
		var n struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				ID               string `json:"id"`
				Type             string `json:"type"`
				Status           string `json:"status"`
				Command          string `json:"command"`
				AggregatedOutput string `json:"aggregatedOutput"`
				ExitCode         *int   `json:"exitCode"`
				Changes          []struct {
					Path string `json:"path"`
				} `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &n) == nil && n.Item.ID != "" {
			status := "completed"
			if strings.EqualFold(n.Item.Status, "failed") || strings.EqualFold(n.Item.Status, "declined") || (n.Item.ExitCode != nil && *n.Item.ExitCode != 0) {
				status = "failed"
			}
			switch n.Item.Type {
			case "commandExecution":
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    n.Item.ID,
					"title":         n.Item.Command,
					"status":        status,
					"rawOutput":     n.Item.AggregatedOutput,
				})
			case "fileChange":
				var paths []string
				for _, ch := range n.Item.Changes {
					if ch.Path != "" {
						paths = append(paths, ch.Path)
					}
				}
				locs := make([]map[string]any, 0, len(paths))
				for _, fp := range paths {
					locs = append(locs, map[string]any{"path": fp})
				}
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    n.Item.ID,
					"title":         "Edit " + strings.Join(paths, ", "),
					"status":        status,
					"locations":     locs,
				})
			case "mcpToolCall", "dynamicToolCall", "webSearch":
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    n.Item.ID,
					"status":        status,
				})
			}
		}
	case "turn/plan/updated":
		var n struct {
			ThreadID string `json:"threadId"`
			Plan     []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if json.Unmarshal(params, &n) == nil && len(n.Plan) > 0 {
			entries := make([]map[string]any, 0, len(n.Plan))
			for _, step := range n.Plan {
				entries = append(entries, map[string]any{
					"content":  step.Step,
					"priority": "medium",
					"status":   step.Status,
				})
			}
			p.emitRawUpdate(n.ThreadID, map[string]any{
				"sessionUpdate": "plan",
				"entries":       entries,
			})
		}
	case "thread/tokenUsage/updated":
		var n struct {
			ThreadID   string `json:"threadId"`
			TokenUsage struct {
				Last struct {
					TotalTokens int64 `json:"totalTokens"`
				} `json:"last"`
				Total struct {
					TotalTokens int64 `json:"totalTokens"`
				} `json:"total"`
				ModelContextWindow int64 `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &n) == nil {
			used := n.TokenUsage.Last.TotalTokens
			if used == 0 {
				used = n.TokenUsage.Total.TotalTokens
			}
			if used > 0 {
				p.emitRawUpdate(n.ThreadID, map[string]any{
					"sessionUpdate": "usage_update",
					"used":          used,
					"size":          n.TokenUsage.ModelContextWindow,
				})
			}
		}
	case "turn/completed":
		var n struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &n) == nil {
			p.mu.Lock()
			act := p.turns[n.ThreadID]
			p.mu.Unlock()
			if act != nil {
				var res codexTurnResult
				switch n.Turn.Status {
				case "interrupted", "cancelled":
					res.stopReason = acp.StopReasonCancelled
				case "failed":
					msg := "Codex turn failed"
					if n.Turn.Error != nil && n.Turn.Error.Message != "" {
						msg = n.Turn.Error.Message
					}
					res.err = errors.New(msg)
				default:
					res.stopReason = acp.StopReasonEndTurn
				}
				select {
				case act.done <- res:
				default:
				}
			}
		}
	case "error":
		var n struct {
			ThreadID string `json:"threadId"`
			Message  string `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(params, &n) == nil {
			msg := n.Message
			if msg == "" && n.Error != nil {
				msg = n.Error.Message
			}
			if msg != "" && n.ThreadID != "" {
				p.mu.Lock()
				act := p.turns[n.ThreadID]
				p.mu.Unlock()
				if act != nil {
					select {
					case act.done <- codexTurnResult{err: errors.New(msg)}:
					default:
					}
				}
			}
		}
	}
}

func (p *codexProtoClient) handleServerRequest(msg jsonRPCMessage) {
	reply := func(result any, errMsg string) {
		resp := jsonRPCMessage{JSONRPC: "2.0", ID: msg.ID}
		if errMsg != "" {
			resp.Error = &jsonRPCError{Code: -32603, Message: errMsg}
		} else {
			raw, _ := json.Marshal(result)
			resp.Result = raw
		}
		_ = p.writeLine(resp)
	}

	switch msg.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "execCommandApproval", "applyPatchApproval":
		// The Firecracker microVM is the safety boundary: always accept
		// immediately if Codex ever sends an approval request.
		reply(map[string]any{"decision": "acceptForSession"}, "")
	default:
		reply(nil, "unsupported method: "+msg.Method)
	}
}

func (p *codexProtoClient) emitTextChunk(threadID, kind, text string) {
	p.emitRawUpdate(threadID, map[string]any{
		"sessionUpdate": kind,
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func (p *codexProtoClient) emitRawUpdate(threadID string, update map[string]any) {
	if p.conn == nil {
		return
	}
	raw, err := json.Marshal(update)
	if err != nil {
		return
	}
	p.conn.emitRawUpdate(threadID, raw)
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ptr[T any](v T) *T { return &v }
