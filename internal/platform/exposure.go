package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
)

// ErrPublic is returned when an agent asks for a public exposure.
var ErrPublic = errors.New("only your owner can make a preview public: expose it as private or authenticated " +
	"and ask your owner to switch it to public in the agenthold UI (agent settings, Preview)")

type exposureInfo struct {
	Port   int64  `json:"port"`
	Name   string `json:"name"`
	Access string `json:"access"`
	URL    string `json:"url"`
}

func (p *Service) exposureInfo(e db.Exposure) exposureInfo {
	return exposureInfo{Port: e.Port, Name: e.Name, Access: e.Access, URL: p.Preview.URL(e)}
}

func (p *Service) expose(ctx context.Context, caller db.Agent, port int, name string, access preview.Access) (string, error) {
	if !p.Preview.Enabled() {
		return "", preview.ErrDisabled
	}
	if access == preview.Public {
		return "", ErrPublic
	}
	if e, err := p.Preview.ByPort(ctx, caller.ID, port); err == nil {
		return fmt.Sprintf("Port %d is already exposed at %s (access: %s).", port, p.Preview.URL(e), e.Access), nil
	}
	e, err := p.Preview.Create(ctx, preview.CreateParams{Agent: caller, Port: port, Name: name, Access: access, CreatedBy: "agent:" + caller.ID})
	if err != nil {
		return "", err
	}
	p.Audit.Record(ctx, audit.Entry{
		AgentID: caller.ID, UserID: caller.OwnerID, Action: audit.ExposeAdd, Target: e.ID,
		Details: map[string]any{"port": e.Port, "name": e.Name, "access": e.Access, "fromAgent": caller.Name, "fromAgentId": caller.ID},
	})
	return fmt.Sprintf("Port %d is exposed at %s (access: %s). Requests reach whatever listens on 127.0.0.1:%d in your machine.",
		port, p.Preview.URL(e), e.Access, port), nil
}

func (p *Service) listExposures(ctx context.Context, caller db.Agent) (string, error) {
	if !p.Preview.Enabled() {
		return "", preview.ErrDisabled
	}
	rows, err := p.Preview.List(ctx, caller.ID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No ports are exposed.", nil
	}
	out := make([]exposureInfo, 0, len(rows))
	for _, e := range rows {
		out = append(out, p.exposureInfo(e))
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func (p *Service) unexpose(ctx context.Context, caller db.Agent, port int) (string, error) {
	if !p.Preview.Enabled() {
		return "", preview.ErrDisabled
	}
	e, err := p.Preview.ByPort(ctx, caller.ID, port)
	if errors.Is(err, preview.ErrNotFound) {
		return "", fmt.Errorf("port %d is not exposed", port)
	}
	if err != nil {
		return "", err
	}
	if err := p.Preview.Delete(ctx, e, caller.OwnerID); err != nil {
		return "", err
	}
	p.Audit.Record(ctx, audit.Entry{
		AgentID: caller.ID, UserID: caller.OwnerID, Action: audit.ExposeRemove, Target: e.ID,
		Details: map[string]any{"port": e.Port, "name": e.Name, "fromAgent": caller.Name, "fromAgentId": caller.ID},
	})
	return fmt.Sprintf("Port %d is no longer exposed.", port), nil
}
