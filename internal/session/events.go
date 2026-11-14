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
	EventPrompt             = "prompt"              // {text, user}
	EventUpdate             = "update"              // raw ACP SessionUpdate
	EventPermission         = "permission"          // {requestId, toolCall, options}
	EventPermissionResolved = "permission_resolved" // {requestId, optionId, by}
	EventTurnEnd            = "turn_end"            // {stopReason}
	EventError              = "error"               // {message}
	EventNotice             = "notice"              // {message}
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
}

func newLog(st *store.Store) *log {
	return &log{store: st, seq: map[string]int64{}, subs: map[string]map[*Subscription]struct{}{}}
}

// append stores an event and notifies subscribers.
func (l *log) append(ctx context.Context, sessionID, typ, userID string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seq, ok := l.seq[sessionID]
	if !ok {
		if seq, err = l.store.MaxSessionSeq(ctx, sessionID); err != nil {
			return err
		}
	}
	seq++
	ev := Event{Seq: seq, At: time.Now().UTC(), Type: typ, UserID: userID, Data: raw}
	if err := l.store.InsertSessionEvent(ctx, db.InsertSessionEventParams{
		SessionID: sessionID, Seq: seq, At: ev.At.UnixMilli(), Type: typ, UserID: userID, Data: string(raw),
	}); err != nil {
		return err
	}
	l.seq[sessionID] = seq
	for s := range l.subs[sessionID] {
		s.push(ev)
	}
	return nil
}

// Viewer is someone watching a session.
type Viewer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	Role      string `json:"role"`
}

// Subscription receives a session's events in order, starting after a cursor.
type Subscription struct {
	C chan Event
	// Presence receives the full list of viewers whenever it changes.
	Presence chan []Viewer
	viewer   *Viewer
	l        *log
	session  string
	once     sync.Once
	// lost is closed if the subscriber fell too far behind; it must resubscribe.
	lost chan struct{}
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
	seen := map[string]bool{}
	out := []Viewer{}
	for s := range l.subs[sessionID] {
		if s.viewer != nil && !seen[s.viewer.ID] {
			seen[s.viewer.ID] = true
			out = append(out, *s.viewer)
		}
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

// subscribe returns stored events after `after` and a subscription for
// everything later. There is no gap and no duplicate between the two.
func (l *log) subscribe(ctx context.Context, sessionID string, after int64, viewer *Viewer) ([]Event, *Subscription, error) {
	s := &Subscription{C: make(chan Event, 1024), Presence: make(chan []Viewer, 1), viewer: viewer, l: l, session: sessionID, lost: make(chan struct{})}
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
