package session

import (
	"context"
	"errors"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/tunnel"
	"github.com/gempir/agenthold/internal/version"
)

// acpSessionModeOptionID is the synthetic config option id used when an ACP
// agent reports session modes outside configOptions (ported from T3 Code's
// sessionConfig.ts).
const acpSessionModeOptionID = "_agenthold/session-mode"

// acpProtoClient speaks the Agent Client Protocol over a tunnel.Process.
type acpProtoClient struct {
	conn *harnessConn
	acp  *acp.ClientSideConnection
}

func newACPProtoClient(c *harnessConn, proc *tunnel.Process) *acpProtoClient {
	ready := make(chan struct{})
	client := acp.NewClientSideConnection(c, proc, &updateCounter{r: proc.Stdout, seen: &c.updatesSeen, ready: ready})
	client.SetLogger(c.m.lg.With("agent", c.agentID, "component", "acp"))
	close(ready)
	return &acpProtoClient{conn: c, acp: client}
}

func (p *acpProtoClient) Initialize(ctx context.Context) (acp.InitializeResponse, []ConfigOption, map[string][]ConfigOption, error) {
	init, err := p.acp.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "agenthold", Version: version.Version},
		ClientCapabilities: acp.ClientCapabilities{Auth: acp.AuthCapabilities{Terminal: true}},
	})
	return init, nil, nil, err
}

func (p *acpProtoClient) Authenticate(ctx context.Context, methodID string) error {
	_, err := p.acp.Authenticate(ctx, acp.AuthenticateRequest{MethodId: methodID})
	return err
}

func (p *acpProtoClient) NewSession(ctx context.Context, cwd string, mcp []acp.McpServer) (string, []ConfigOption, error) {
	res, err := p.acp.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: mcp})
	if err != nil {
		return "", nil, err
	}
	opts := mergeACPModesAndModels(fromACP(res.ConfigOptions), res.Modes, p.conn.adapter.CustomModelsList)
	return string(res.SessionId), opts, nil
}

func (p *acpProtoClient) ResumeSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	res, err := p.acp.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: acp.SessionId(sessionID), Cwd: cwd, McpServers: mcp})
	if err != nil {
		return nil, err
	}
	return mergeACPModesAndModels(fromACP(res.ConfigOptions), res.Modes, p.conn.adapter.CustomModelsList), nil
}

func (p *acpProtoClient) LoadSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error) {
	res, err := p.acp.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(sessionID), Cwd: cwd, McpServers: mcp})
	if err != nil {
		return nil, err
	}
	return mergeACPModesAndModels(fromACP(res.ConfigOptions), res.Modes, p.conn.adapter.CustomModelsList), nil
}

func (p *acpProtoClient) CloseSession(ctx context.Context, sessionID string) error {
	_, err := p.acp.CloseSession(ctx, acp.CloseSessionRequest{SessionId: acp.SessionId(sessionID)})
	return err
}

func (p *acpProtoClient) SetConfigOption(ctx context.Context, sessionID string, opt ConfigOption, value string) ([]ConfigOption, error) {
	if opt.ID == acpSessionModeOptionID {
		if _, err := p.acp.SetSessionMode(ctx, acp.SetSessionModeRequest{
			SessionId: acp.SessionId(sessionID),
			ModeId:    acp.SessionModeId(value),
		}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	req := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: acp.SessionId(sessionID), ConfigId: acp.SessionConfigId(opt.ID), Value: acp.SessionConfigValueId(value),
	}}
	if opt.Type == "boolean" {
		req = acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
			SessionId: acp.SessionId(sessionID), ConfigId: acp.SessionConfigId(opt.ID), Value: value == "true", Type: "boolean",
		}}
	}
	res, err := p.acp.SetSessionConfigOption(ctx, req)
	if err != nil {
		return nil, err
	}
	if res.ConfigOptions == nil {
		return nil, nil
	}
	return mergeACPModesAndModels(fromACP(res.ConfigOptions), nil, p.conn.adapter.CustomModelsList), nil
}

func (p *acpProtoClient) InjectHistory(_ context.Context, _, _ string, _ HandoffSelection) (bool, error) {
	return false, nil
}

// isAgentBusyPromptError matches ACP agents (such as pi-agent-core / oh-my-pi)
// that reject a prompt while background work finishes; ported from T3 Code's
// AcpAdapterV2.
func isAgentBusyPromptError(err error) bool {
	var re *acp.RequestError
	return errors.As(err, &re) && strings.HasPrefix(re.Message, "Agent is already processing")
}

func (p *acpProtoClient) Prompt(ctx context.Context, sessionID, text string) (acp.PromptResponse, error) {
	for attempt := 0; ; attempt++ {
		res, err := p.acp.Prompt(ctx, acp.PromptRequest{
			SessionId: acp.SessionId(sessionID),
			Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
		})
		if err == nil || !isAgentBusyPromptError(err) || attempt >= 30 || ctx.Err() != nil {
			return res, err
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return acp.PromptResponse{}, ctx.Err()
		}
	}
}

func (p *acpProtoClient) Cancel(ctx context.Context, sessionID string) error {
	return p.acp.Cancel(ctx, acp.CancelNotification{SessionId: acp.SessionId(sessionID)})
}

func (p *acpProtoClient) Done() <-chan struct{} { return p.acp.Done() }
func (p *acpProtoClient) Close()                {}

// mergeACPModesAndModels enriches ACP configOptions with SessionModeState
// and user-configured custom model IDs.
func mergeACPModesAndModels(opts []ConfigOption, modes *acp.SessionModeState, customModels []string) []ConfigOption {
	for i := range opts {
		if opts[i].Kind == KindModel {
			for _, cm := range customModels {
				cm = strings.TrimSpace(cm)
				if cm != "" && !opts[i].Has(cm) {
					opts[i].Values = append(opts[i].Values, ConfigValue{Value: cm, Name: cm})
				}
			}
		}
	}
	hasMode := false
	for _, o := range opts {
		if o.Category == "mode" {
			hasMode = true
			break
		}
	}
	if !hasMode && modes != nil && len(modes.AvailableModes) > 1 {
		mo := ConfigOption{
			ID:          acpSessionModeOptionID,
			Name:        "Mode",
			Description: "Session mode advertised by the ACP agent.",
			Category:    "mode",
			Type:        "select",
			Current:     string(modes.CurrentModeId),
		}
		for _, m := range modes.AvailableModes {
			mo.Values = append(mo.Values, ConfigValue{
				Value:       string(m.Id),
				Name:        m.Name,
				Description: deref(m.Description),
			})
		}
		opts = append(opts, mo)
	}
	return opts
}
