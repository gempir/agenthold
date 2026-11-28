package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gempir/agenthold/internal/store/db"
)

// Visual and MCP App payload bounds.
const (
	MaxVisualHTMLBytes = 512 * 1024 // 512 KiB
	MinVisualHeight    = 120
	MaxVisualHeight    = 900
	MaxVisualTitleLen  = 200
	MaxMCPAppActions   = 50
)

// Visual errors.
var (
	ErrVisualEmpty    = errors.New("html is required")
	ErrVisualTooLarge = fmt.Errorf("html exceeds the %d KiB limit", MaxVisualHTMLBytes/1024)
)

// VisualPayload is the structured payload stored in an EventVisual session event.
type VisualPayload struct {
	Title  string `json:"title,omitempty"`
	HTML   string `json:"html"`
	Height int    `json:"height,omitempty"`
}

// MCPAppAction describes an interactive action exposed by an MCP App card.
// Any action not explicitly marked ReadOnly triggers a user confirmation gate
// in the UI before executing.
type MCPAppAction struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
}

// MCPAppPayload is the structured payload stored in an EventMCPApp session event.
type MCPAppPayload struct {
	Title       string         `json:"title"`
	HTML        string         `json:"html"`
	Server      string         `json:"server,omitempty"`
	ToolCallID  string         `json:"toolCallId,omitempty"`
	ResourceURI string         `json:"resourceUri,omitempty"`
	Actions     []MCPAppAction `json:"actions,omitempty"`
	Height      int            `json:"height,omitempty"`
}

// VisualDocument is a stored visual or MCP App HTML document fetched by event sequence.
type VisualDocument struct {
	Seq   int64
	Type  string
	Title string
	HTML  string
}

// ValidateVisualHTML validates self-contained HTML for visual replies and MCP Apps.
func ValidateVisualHTML(html string) error {
	if strings.TrimSpace(html) == "" {
		return ErrVisualEmpty
	}
	if len(html) > MaxVisualHTMLBytes {
		return ErrVisualTooLarge
	}
	return nil
}

// ClampVisualHeight clamps a requested height to [MinVisualHeight, MaxVisualHeight].
// Non-positive values return 0 to indicate auto-sizing.
func ClampVisualHeight(h int) int {
	if h <= 0 {
		return 0
	}
	if h < MinVisualHeight {
		return MinVisualHeight
	}
	if h > MaxVisualHeight {
		return MaxVisualHeight
	}
	return h
}

func cleanVisualText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return s
}

// RecordVisual validates and appends an inline HTML visual event to sessionID's
// append-only event log, broadcasting it live to all subscribed viewers.
func (m *Manager) RecordVisual(ctx context.Context, sessionID string, payload VisualPayload) (int64, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, ErrNotFound
	}
	if err := ValidateVisualHTML(payload.HTML); err != nil {
		return 0, err
	}
	if _, err := m.store.GetAgentSession(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	cleaned := VisualPayload{
		Title:  cleanVisualText(payload.Title, MaxVisualTitleLen),
		HTML:   payload.HTML,
		Height: ClampVisualHeight(payload.Height),
	}
	return m.log.appendSeq(ctx, sessionID, EventVisual, "", cleaned)
}

// RecordMCPApp validates and appends an interactive MCP App UI event to
// sessionID's append-only event log, broadcasting it live to all viewers.
func (m *Manager) RecordMCPApp(ctx context.Context, sessionID string, payload MCPAppPayload) (int64, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, ErrNotFound
	}
	if err := ValidateVisualHTML(payload.HTML); err != nil {
		return 0, err
	}
	if _, err := m.store.GetAgentSession(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	title := cleanVisualText(payload.Title, MaxVisualTitleLen)
	if title == "" {
		title = "MCP App"
	}
	cleaned := MCPAppPayload{
		Title:       title,
		HTML:        payload.HTML,
		Server:      cleanVisualText(payload.Server, 120),
		ToolCallID:  cleanVisualText(payload.ToolCallID, 200),
		ResourceURI: cleanVisualText(payload.ResourceURI, 512),
		Height:      ClampVisualHeight(payload.Height),
	}
	for _, a := range payload.Actions {
		name := cleanVisualText(a.Name, 120)
		if name == "" {
			continue
		}
		cleaned.Actions = append(cleaned.Actions, MCPAppAction{
			Name:        name,
			Label:       cleanVisualText(a.Label, 120),
			Description: cleanVisualText(a.Description, 300),
			ReadOnly:    a.ReadOnly,
		})
		if len(cleaned.Actions) >= MaxMCPAppActions {
			break
		}
	}
	return m.log.appendSeq(ctx, sessionID, EventMCPApp, "", cleaned)
}

// GetVisualEvent loads a stored EventVisual or EventMCPApp document by sequence number.
func (m *Manager) GetVisualEvent(ctx context.Context, sessionID string, seq int64) (VisualDocument, error) {
	if seq <= 0 {
		return VisualDocument{}, ErrEventNotFound
	}
	row, err := m.store.GetSessionEvent(ctx, db.GetSessionEventParams{
		SessionID: sessionID,
		Seq:       seq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return VisualDocument{}, ErrEventNotFound
	}
	if err != nil {
		return VisualDocument{}, err
	}
	switch row.Type {
	case EventVisual:
		var p VisualPayload
		if err := json.Unmarshal([]byte(row.Data), &p); err != nil || strings.TrimSpace(p.HTML) == "" {
			return VisualDocument{}, ErrEventNotFound
		}
		return VisualDocument{Seq: row.Seq, Type: row.Type, Title: p.Title, HTML: p.HTML}, nil
	case EventMCPApp:
		var p MCPAppPayload
		if err := json.Unmarshal([]byte(row.Data), &p); err != nil || strings.TrimSpace(p.HTML) == "" {
			return VisualDocument{}, ErrEventNotFound
		}
		return VisualDocument{Seq: row.Seq, Type: row.Type, Title: p.Title, HTML: p.HTML}, nil
	default:
		return VisualDocument{}, ErrEventNotFound
	}
}
