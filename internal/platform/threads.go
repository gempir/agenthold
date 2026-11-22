package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// threadTools act on the calling agent's own threads (sessions).
var threadTools = []map[string]any{
	{
		"name": "list_threads",
		"description": "List your threads (conversations with you), newest activity first: active ones, and settled " +
			"ones too with include_settled. The thread you are in is marked current.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"include_settled": map[string]any{"type": "boolean", "description": "Also list settled threads (default false)."},
			},
		},
	},
	{
		"name": "settle_thread",
		"description": "Settle a thread of yours: mark it done so it leaves your users' active list. Use it when the " +
			"work of a thread is finished. Without thread_id it settles the thread you are in. A new message unsettles it.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "The thread's id (see list_threads); default: the current thread."},
			},
		},
	},
	{
		"name":        "unsettle_thread",
		"description": "Move a settled thread of yours back into the active list.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"thread_id"},
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "The thread's id (see list_threads)."},
			},
		},
	},
	{
		"name": "read_thread",
		"description": "Read conversation history from one of your threads (default: the current thread). " +
			"Use seq to inspect a single omitted handoff item, or after_seq + limit to paginate. " +
			"Long items return next_offset; pass offset=next_offset to read the rest.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "Thread id (default: current thread)."},
				"seq":       map[string]any{"type": "integer", "description": "Read one specific historical item by its seq id."},
				"after_seq": map[string]any{"type": "integer", "description": "List items with seq > after_seq (default 0)."},
				"limit":     map[string]any{"type": "integer", "description": "Maximum items to return (default 20, max 100)."},
				"max_chars": map[string]any{"type": "integer", "description": "Maximum characters per item text (default 4000, max 20000)."},
				"offset":    map[string]any{"type": "integer", "description": "Character offset into a single item when seq is set."},
			},
		},
	},
}

// maxListedThreads bounds list_threads.
const maxListedThreads = 100

// ErrNoThread is returned for threads that are not the caller's own.
var ErrNoThread = errors.New("no thread of yours with that id")

type threadInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Running   bool   `json:"running,omitempty"`
	Current   bool   `json:"current,omitempty"`
	Settled   bool   `json:"settled"`
	SettledAt string `json:"settledAt,omitempty"`
}

func rfc3339(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

func (p *Service) listThreads(ctx context.Context, caller db.Agent, current string, includeSettled bool) (string, error) {
	rows, err := p.Store.ListAgentSessions(ctx, caller.ID)
	if err != nil {
		return "", err
	}
	out := []threadInfo{}
	more := 0
	for _, r := range rows {
		if r.SettledAt.Valid && !includeSettled {
			continue
		}
		if len(out) == maxListedThreads {
			more++
			continue
		}
		t := threadInfo{ID: r.ID, Title: r.Title, UpdatedAt: rfc3339(r.UpdatedAt), Current: r.ID == current, Settled: r.SettledAt.Valid}
		if p.Sessions != nil {
			t.Running = p.Sessions.Running(r.ID)
		}
		if r.SettledAt.Valid {
			t.SettledAt = rfc3339(r.SettledAt.Int64)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		if includeSettled {
			return "You have no threads.", nil
		}
		return "You have no active threads.", nil
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if more > 0 {
		return fmt.Sprintf("%s\n(%d older threads not listed)", b, more), nil
	}
	return string(b), nil
}

// ownThread loads one of caller's sessions; id defaults to current.
func (p *Service) ownThread(ctx context.Context, caller db.Agent, id, current string) (db.AgentSession, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = current
	}
	if id == "" {
		return db.AgentSession{}, errors.New("thread_id is required")
	}
	s, err := p.Store.GetAgentSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.AgentID != caller.ID) {
		// Other agents' threads are indistinguishable from missing ones.
		return db.AgentSession{}, ErrNoThread
	}
	return s, err
}

func (p *Service) settleThread(ctx context.Context, caller db.Agent, id, current string) (string, error) {
	s, err := p.ownThread(ctx, caller, id, current)
	if err != nil {
		return "", err
	}
	s, changed, err := session.Settle(ctx, p.Store, s.ID, session.SettledByAgent(caller.ID), time.Now())
	if err != nil {
		return "", err
	}
	if !changed {
		return fmt.Sprintf("Thread %s (%q) is settled already.", s.ID, s.Title), nil
	}
	p.agentAudit(ctx, caller, caller.ID, audit.SessionSettle, s.ID, map[string]any{"session": s.Title})
	return fmt.Sprintf("Settled thread %s (%q). A new message in it makes it active again.", s.ID, s.Title), nil
}

func (p *Service) unsettleThread(ctx context.Context, caller db.Agent, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("thread_id is required")
	}
	s, err := p.ownThread(ctx, caller, id, "")
	if err != nil {
		return "", err
	}
	s, changed, err := session.Unsettle(ctx, p.Store, s.ID, time.Now())
	if err != nil {
		return "", err
	}
	if !changed {
		return fmt.Sprintf("Thread %s (%q) is active already.", s.ID, s.Title), nil
	}
	p.agentAudit(ctx, caller, caller.ID, audit.SessionUnsettle, s.ID, map[string]any{"session": s.Title})
	return fmt.Sprintf("Thread %s (%q) is active again.", s.ID, s.Title), nil
}

type readThreadItem struct {
	Seq        int64  `json:"seq"`
	Role       string `json:"role"`
	Kind       string `json:"kind"`
	Status     string `json:"status,omitempty"`
	Text       string `json:"text"`
	NextOffset *int   `json:"next_offset,omitempty"`
}

func (p *Service) readThread(ctx context.Context, caller db.Agent, current string, in toolArgs) (string, error) {
	s, err := p.ownThread(ctx, caller, in.ThreadID, current)
	if err != nil {
		return "", err
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = 4000
	}
	if maxChars > 20000 {
		maxChars = 20000
	}
	msgs, err := session.ReconstructHistory(ctx, p.Store, s.ID, 0, 0)
	if err != nil {
		return "", err
	}
	clipItem := func(m session.HistoricalMessage, offset int) readThreadItem {
		runes := []rune(m.Text)
		if offset < 0 {
			offset = 0
		}
		if offset > len(runes) {
			offset = len(runes)
		}
		end := min(len(runes), offset+maxChars)
		item := readThreadItem{
			Seq:    m.Seq,
			Role:   m.Role,
			Kind:   m.Kind,
			Status: m.Status,
			Text:   string(runes[offset:end]),
		}
		if end < len(runes) {
			next := end
			item.NextOffset = &next
		}
		return item
	}

	if in.Seq > 0 {
		for _, m := range msgs {
			if m.Seq == in.Seq {
				b, _ := json.MarshalIndent(map[string]any{
					"thread_id": s.ID,
					"title":     s.Title,
					"item":      clipItem(m, in.Offset),
				}, "", "  ")
				return string(b), nil
			}
		}
		return "", fmt.Errorf("no historical item with seq %d in thread %s", in.Seq, s.ID)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var items []readThreadItem
	var nextAfterSeq int64
	hasMore := false
	for _, m := range msgs {
		if m.Seq <= in.AfterSeq {
			continue
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, clipItem(m, 0))
		nextAfterSeq = m.Seq
	}
	resp := map[string]any{
		"thread_id": s.ID,
		"title":     s.Title,
		"items":     items,
	}
	if hasMore {
		resp["next_after_seq"] = nextAfterSeq
	}
	b, _ := json.MarshalIndent(resp, "", "  ")
	return string(b), nil
}
