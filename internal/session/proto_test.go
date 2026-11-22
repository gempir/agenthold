package session

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestCodexProtoClientLifecycleAndNativeHandoff(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()

	var injectedItems []map[string]any
	var sawInterrupt bool

	// Mock codex app-server peer.
	go func() {
		br := bufio.NewReader(srv)
		write := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = srv.Write(append(b, '\n'))
		}
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg jsonRPCMessage
			if json.Unmarshal(line, &msg) != nil || msg.Method == "" {
				continue
			}
			switch msg.Method {
			case "initialize":
				write(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"result": map[string]any{"version": "0.171.0"},
				})
			case "model/list":
				write(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"result": map[string]any{
						"data": []map[string]any{
							{
								"model":                  "gpt-5.4",
								"displayName":            "GPT-5.4",
								"isDefault":              true,
								"defaultReasoningEffort": "medium",
								"supportedReasoningEfforts": []map[string]any{
									{"reasoningEffort": "low"},
									{"reasoningEffort": "medium"},
									{"reasoningEffort": "high"},
								},
							},
						},
					},
				})
			case "thread/start":
				write(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"result": map[string]any{"thread": map[string]any{"id": "thr_codex_1"}},
				})
			case "thread/inject_items":
				var p struct {
					ThreadID string           `json:"threadId"`
					Items    []map[string]any `json:"items"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				injectedItems = p.Items
				write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
			case "turn/start":
				write(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"result": map[string]any{"turn": map[string]any{"id": "turn_1"}},
				})
				// Stream deltas, usage, and completion.
				write(map[string]any{
					"jsonrpc": "2.0", "method": "item/agentMessage/delta",
					"params": map[string]any{"threadId": "thr_codex_1", "delta": "Hello from Codex"},
				})
				write(map[string]any{
					"jsonrpc": "2.0", "method": "thread/tokenUsage/updated",
					"params": map[string]any{
						"threadId": "thr_codex_1",
						"tokenUsage": map[string]any{
							"last":               map[string]any{"totalTokens": 1200},
							"modelContextWindow": 200000,
						},
					},
				})
				write(map[string]any{
					"jsonrpc": "2.0", "method": "turn/completed",
					"params": map[string]any{"threadId": "thr_codex_1", "turn": map[string]any{"id": "turn_1", "status": "completed"}},
				})
			case "turn/interrupt":
				sawInterrupt = true
				write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
			}
		}
	}()

	client := newCodexProtoClientRW(nil, cli)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initResp, opts, byModel, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initResp.AgentInfo == nil || initResp.AgentInfo.Version != "0.171.0" {
		t.Fatalf("AgentInfo = %+v", initResp.AgentInfo)
	}
	if len(opts) < 2 || opts[0].Current != "gpt-5.4" || len(byModel["gpt-5.4"]) < 2 {
		t.Fatalf("opts=%+v byModel=%+v", opts, byModel)
	}

	sid, sessOpts, err := client.NewSession(ctx, "/home/agent", nil)
	if err != nil || sid != "thr_codex_1" || len(sessOpts) < 2 {
		t.Fatalf("NewSession: sid=%q opts=%+v err=%v", sid, sessOpts, err)
	}

	sel := SelectHistory("ses_1", "claude-code", "codex", []HistoricalMessage{
		{Role: "user", Kind: "user_message", Text: "earlier question", Seq: 1, Status: "completed"},
		{Role: "assistant", Kind: "assistant_message", Text: "earlier answer", Seq: 2, Status: "completed"},
	}, 8000)
	ok, err := client.InjectHistory(ctx, sid, "ses_1", sel)
	if err != nil || !ok {
		t.Fatalf("InjectHistory: ok=%v err=%v", ok, err)
	}
	if len(injectedItems) != 3 { // context + 2 messages
		t.Fatalf("expected 3 injected items, got %d", len(injectedItems))
	}

	res, err := client.Prompt(ctx, sid, "continue")
	if err != nil || res.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt: res=%+v err=%v", res, err)
	}

	// Verify Cancel sends turn/interrupt when active.
	client.mu.Lock()
	client.turns[sid] = &codexActiveTurn{turnID: "turn_99", done: make(chan codexTurnResult, 1)}
	client.mu.Unlock()
	if err := client.Cancel(ctx, sid); err != nil || !sawInterrupt {
		t.Fatalf("Cancel: err=%v sawInterrupt=%v", err, sawInterrupt)
	}
}

func TestClaudeProtoClientLifecycle(t *testing.T) {
	mockPeer := func() io.ReadWriteCloser {
		cli, srv := net.Pipe()
		go func() {
			defer srv.Close()
			br := bufio.NewReader(srv)
			write := func(v any) {
				b, _ := json.Marshal(v)
				_, _ = srv.Write(append(b, '\n'))
			}
			for {
				line, err := br.ReadBytes('\n')
				if err != nil {
					return
				}
				var msg struct {
					Type      string `json:"type"`
					RequestID string `json:"request_id"`
					Request   struct {
						Subtype string `json:"subtype"`
						Model   string `json:"model"`
					} `json:"request"`
				}
				if json.Unmarshal(line, &msg) != nil {
					continue
				}
				switch msg.Type {
				case "control_request":
					switch msg.Request.Subtype {
					case "initialize":
						write(map[string]any{
							"type": "control_response",
							"response": map[string]any{
								"subtype":    "success",
								"request_id": msg.RequestID,
								"response": map[string]any{
									"claude_code_version": "2.1.330",
									"commands": []map[string]any{
										{"name": "compact", "description": "Compact context"},
									},
									"models": []map[string]any{
										{
											"value":                 "opus",
											"displayName":           "Opus 5.5",
											"supportsEffort":        true,
											"supportedEffortLevels": []string{"low", "medium", "high", "max"},
											"supportsFastMode":      true,
										},
									},
								},
							},
						})
					case "set_model", "set_permission_mode", "interrupt":
						write(map[string]any{
							"type": "control_response",
							"response": map[string]any{
								"subtype":    "success",
								"request_id": msg.RequestID,
								"response":   map[string]any{},
							},
						})
					}
				case "user":
					write(map[string]any{
						"type": "stream_event",
						"event": map[string]any{
							"type":  "content_block_delta",
							"delta": map[string]any{"type": "text_delta", "text": "Hello from Claude"},
						},
					})
					write(map[string]any{
						"type":           "result",
						"subtype":        "success",
						"is_error":       false,
						"total_cost_usd": 0.01,
						"usage":          map[string]any{"input_tokens": 500, "output_tokens": 100},
					})
				}
			}
		}()
		return cli
	}

	client := &claudeProtoClient{
		procs: map[string]*claudeProc{},
		cfg:   map[string]*claudeSessionCfg{},
		done:  make(chan struct{}),
		spawn: func(context.Context, string, []string) (io.ReadWriteCloser, error) {
			return mockPeer(), nil
		},
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initResp, opts, byModel, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initResp.AgentInfo == nil || initResp.AgentInfo.Version != "2.1.330" {
		t.Fatalf("AgentInfo = %+v", initResp.AgentInfo)
	}
	if len(opts) < 3 || opts[0].Current != "opus" || len(byModel["opus"]) != 2 {
		t.Fatalf("opts=%+v byModel=%+v", opts, byModel)
	}
	if len(client.commands) != 1 || client.commands[0].Name != "compact" {
		t.Fatalf("commands = %+v", client.commands)
	}

	sid, _, err := client.NewSession(ctx, "/home/agent", nil)
	if err != nil || !strings.HasPrefix(sid, "cl_") {
		t.Fatalf("NewSession: sid=%q err=%v", sid, err)
	}
	res, err := client.Prompt(ctx, sid, "hi")
	if err != nil || res.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt: res=%+v err=%v", res, err)
	}
	if err := client.Cancel(ctx, sid); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
