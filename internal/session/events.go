// Package session runs conversations with agents. The server is the ACP
// client: one connection per agent's harness, many sessions on it. Every
// update is appended to a session's event log and fanned out to viewers,
// who resume from a sequence number after reconnecting.
package session

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Event types.
const (
	EventPrompt                = "prompt"                  // {text, user}
	EventUpdate                = "update"                  // raw ACP SessionUpdate
	EventPermission            = "permission"              // {requestId, toolCall, options}
	EventPermissionResolved    = "permission_resolved"     // {requestId, optionId, by}
	EventTurnEnd               = "turn_end"                // {stopReason}
	EventError                 = "error"                   // {message}
	EventNotice                = "notice"                  // {message}
	EventSubthread             = "subthread"               // {sessionId, agentId, title, harness, model, status, summary}
	EventSecretRequest         = "secret_request"          // {requestId, name, description, hosts, envVar?, header?}
	EventSecretRequestResolved = "secret_request_resolved" // {requestId, status, secretId?, name?, envVar?, placeholder?, by}
	EventAccountConnectRequest = "account_connect_request" // {requestId, toolId, slug, name, description?, kind, authMode, reason?, connected, connectEndpoint}
	EventVisual                = "visual"                  // {title, html, height}
	EventMCPApp                = "mcp_app"                 // {title, html, server, toolCallId, resourceUri, actions, height}
)

// Event is one entry in a session's log.
type Event struct {
	Seq    int64           `json:"seq"`
	At     time.Time       `json:"at"`
	Type   string          `json:"type"`
	UserID string          `json:"userId,omitempty"`
	Data   json.RawMessage `json:"data"`
}

// log appends events and fans them out.
type log struct {
	store *store.Store

	mu   sync.Mutex
	seq  map[string]int64
	subs map[string]map[*Subscription]struct{}
	// observers see every appended event (Manager.Observe).
	observers []func(sessionID string, ev Event)
}

func newLog(st *store.Store) *log {
	return &log{store: st, seq: map[string]int64{}, subs: map[string]map[*Subscription]struct{}{}}
}

// append stores an event and notifies subscribers.
func (l *log) append(ctx context.Context, sessionID, typ, userID string, data any) error {
	_, err := l.appendSeq(ctx, sessionID, typ, userID, data)
	return err
}

// appendSeq stores an event, notifies subscribers, and returns its sequence number.
func (l *log) appendSeq(ctx context.Context, sessionID, typ, userID string, data any) (int64, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seq, ok := l.seq[sessionID]
	if !ok {
		if seq, err = l.store.MaxSessionSeq(ctx, sessionID); err != nil {
			return 0, err
		}
	}
	seq++
	ev := Event{Seq: seq, At: time.Now().UTC(), Type: typ, UserID: userID, Data: raw}
	if err := l.store.InsertSessionEvent(ctx, db.InsertSessionEventParams{
		SessionID: sessionID, Seq: seq, At: ev.At.UnixMilli(), Type: typ, UserID: userID, Data: string(raw),
	}); err != nil {
		return 0, err
	}
	l.seq[sessionID] = seq
	for s := range l.subs[sessionID] {
		s.push(ev)
	}
	for _, fn := range l.observers {
		fn(sessionID, ev)
	}
	return seq, nil
}

// Viewer is someone watching a session.
type Viewer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Role      string `json:"role"`
	Activity  string `json:"activity,omitempty"` // "idle" | "typing" | "viewing_diff"
}

// NormalizeViewerActivity returns a valid presence activity string.
func NormalizeViewerActivity(a string) string {
	switch a {
	case "typing", "viewing_diff":
		return a
	default:
		return "idle"
	}
}

func activityRank(a string) int {
	switch a {
	case "typing":
		return 2
	case "viewing_diff":
		return 1
	default:
		return 0
	}
}

// StreamNotice is an ephemeral real-time frame broadcast to session subscribers
// (e.g. guest_request, review_comments_updated).
type StreamNotice struct {
	Type    string
	Payload map[string]any
}

// Subscription receives a session's events in order, starting after a cursor.
type Subscription struct {
	C chan Event
	// Presence receives the full list of viewers whenever it changes.
	Presence chan []Viewer
	// Queue receives the full queued message list whenever the queue changes.
	Queue chan QueueUpdate
	// Notice receives ephemeral real-time notifications (guest requests, review comments).
	Notice  chan StreamNotice
	viewer  *Viewer
	l       *log
	session string
	once    sync.Once
	// lost is closed if the subscriber fell too far behind; it must resubscribe.
	lost chan struct{}
}

// SetActivity updates this subscriber's presence activity and broadcasts the
// updated viewer list to all subscribers of the session.
func (s *Subscription) SetActivity(activity string) {
	if s == nil || s.viewer == nil {
		return
	}
	norm := NormalizeViewerActivity(activity)
	s.l.mu.Lock()
	if s.viewer.Activity == norm {
		s.l.mu.Unlock()
		return
	}
	s.viewer.Activity = norm
	s.l.broadcastPresence(s.session)
	s.l.mu.Unlock()
}

func (s *Subscription) push(ev Event) {
	select {
	case s.C <- ev:
	default:
		s.once.Do(func() { close(s.lost) })
	}
}

// Lost is closed when events were dropped because the subscriber was slow.
func (s *Subscription) Lost() <-chan struct{} { return s.lost }

// Close ends the subscription.
func (s *Subscription) Close() {
	s.l.mu.Lock()
	delete(s.l.subs[s.session], s)
	if s.viewer != nil {
		s.l.broadcastPresence(s.session)
	}
	s.l.mu.Unlock()
}

// viewers lists distinct viewers of a session. Callers hold l.mu.
func (l *log) viewers(sessionID string) []Viewer {
	byID := map[string]Viewer{}
	for s := range l.subs[sessionID] {
		if s.viewer == nil {
			continue
		}
		v := *s.viewer
		if v.Activity == "" {
			v.Activity = "idle"
		}
		if prev, ok := byID[v.ID]; !ok || activityRank(v.Activity) > activityRank(prev.Activity) {
			byID[v.ID] = v
		}
	}
	out := make([]Viewer, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// broadcastPresence sends the viewer list to every subscriber. Callers hold l.mu.
func (l *log) broadcastPresence(sessionID string) {
	vs := l.viewers(sessionID)
	for s := range l.subs[sessionID] {
		select {
		case <-s.Presence: // drop a stale list; only the latest matters
		default:
		}
		select {
		case s.Presence <- vs:
		default:
		}
	}
}

// broadcastQueue sends the latest queued messages snapshot to every subscriber.
func (l *log) broadcastQueue(sessionID string, update QueueUpdate) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for s := range l.subs[sessionID] {
		select {
		case <-s.Queue: // drop stale snapshot; only the latest matters
		default:
		}
		select {
		case s.Queue <- update:
		default:
		}
	}
}

// broadcastNotice sends an ephemeral real-time frame to every subscriber.
func (l *log) broadcastNotice(sessionID string, notice StreamNotice) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for s := range l.subs[sessionID] {
		select {
		case s.Notice <- notice:
		default:
		}
	}
}

// rewind deletes stored events with seq >= fromSeq and updates the cached seq.
func (l *log) rewind(ctx context.Context, sessionID string, fromSeq int64) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	deleted, err := l.store.DeleteSessionEventsFrom(ctx, db.DeleteSessionEventsFromParams{
		SessionID: sessionID,
		Seq:       fromSeq,
	})
	if err != nil {
		return 0, err
	}
	maxSeq, err := l.store.MaxSessionSeq(ctx, sessionID)
	if err != nil {
		delete(l.seq, sessionID)
		return deleted, err
	}
	l.seq[sessionID] = maxSeq
	return deleted, nil
}

// subscribe returns stored events after `after` and a subscription for
// everything later. There is no gap and no duplicate between the two.
func (l *log) subscribe(ctx context.Context, sessionID string, after int64, viewer *Viewer) ([]Event, *Subscription, error) {
	if viewer != nil && viewer.Activity == "" {
		v := *viewer
		v.Activity = "idle"
		viewer = &v
	}
	s := &Subscription{C: make(chan Event, 1024), Presence: make(chan []Viewer, 1), Queue: make(chan QueueUpdate, 1), Notice: make(chan StreamNotice, 32), viewer: viewer, l: l, session: sessionID, lost: make(chan struct{})}
	l.mu.Lock()
	defer l.mu.Unlock()
	var backlog []Event
	for {
		rows, err := l.store.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: sessionID, Seq: after, Limit: 1000})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			backlog = append(backlog, eventFromRow(r))
			after = r.Seq
		}
		if len(rows) < 1000 {
			break
		}
	}
	if l.subs[sessionID] == nil {
		l.subs[sessionID] = map[*Subscription]struct{}{}
	}
	l.subs[sessionID][s] = struct{}{}
	l.broadcastPresence(sessionID)
	return backlog, s, nil
}

func eventFromRow(r db.AgentSessionEvent) Event {
	return Event{Seq: r.Seq, At: time.UnixMilli(r.At).UTC(), Type: r.Type, UserID: r.UserID, Data: json.RawMessage(r.Data)}
}
