// Package activity keeps a short, live log per agent of what the platform is
// doing for it right now: waking its machine (resume or cold boot and why),
// the guest's boot phases, sandboxd's tunnel attempts and refusals, the
// harness start (spawn, ACP initialize, session setup, with timings) and the
// harness's stderr. It is what the chat shows when a wake or a harness start
// takes long, so a user can see where it is stuck.
//
// Entries come from the server's structured log: Tap wraps the slog handler
// and copies every record that names an agent ("agent" or "sandbox" attribute
// with an agent id) into that agent's log, including debug records the
// server's own log does not print. The log is in memory, per controller, and
// bounded; it is not an audit trail.
package activity

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Keep is how many entries are kept per agent.
const Keep = 400

// maxMessage bounds one entry's text.
const maxMessage = 2000

// Entry is one line of an agent's activity.
type Entry struct {
	Seq     int64
	At      time.Time
	Level   string // debug, info, warn, error
	Source  string // what wrote it: wake, guest, tunnel, harness, stderr, ...
	Message string
}

// Log holds the activity of every agent.
type Log struct {
	mu     sync.Mutex
	seq    int64
	agents map[string][]Entry
}

// New returns an empty log.
func New() *Log { return &Log{agents: map[string][]Entry{}} }

// Add appends an entry for agentID.
func (l *Log) Add(agentID string, level slog.Level, source, message string) {
	if l == nil || agentID == "" {
		return
	}
	if len(message) > maxMessage {
		message = strings.ToValidUTF8(message[:maxMessage], "") + "…"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.agents[agentID] = append(l.agents[agentID], Entry{Seq: l.seq, At: time.Now(), Level: levelName(level), Source: source, Message: message})
	if es := l.agents[agentID]; len(es) > Keep {
		l.agents[agentID] = append([]Entry(nil), es[len(es)-Keep:]...)
	}
}

// Since returns agentID's entries after seq (0: all kept), oldest first.
func (l *Log) Since(agentID string, after int64) []Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	es := l.agents[agentID]
	i := len(es)
	for i > 0 && es[i-1].Seq > after {
		i--
	}
	return append([]Entry(nil), es[i:]...)
}

// Last describes agentID's latest warning or error since a time, or else its
// latest entry ("" if none): what a timed-out wait reports.
func (l *Log) Last(agentID string, since time.Time) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	es := l.agents[agentID]
	var last *Entry
	for i := len(es) - 1; i >= 0 && !es[i].At.Before(since); i-- {
		if es[i].Level == "warn" || es[i].Level == "error" {
			return es[i].Message
		}
		if last == nil {
			last = &es[i]
		}
	}
	if last == nil {
		return ""
	}
	return last.Message
}

// Forget drops an agent's entries (it was deleted).
func (l *Log) Forget(agentID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.agents, agentID)
	l.mu.Unlock()
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	}
	return "error"
}

// Tap returns a handler that passes records on to next and copies those that
// name an agent into l. Records below next's level still reach l.
func Tap(next slog.Handler, l *Log) slog.Handler { return &tap{next: next, log: l} }

type tap struct {
	next  slog.Handler
	log   *Log
	attrs []slog.Attr // from WithAttrs (groups are flattened)
}

func (t *tap) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug || t.next.Enabled(ctx, level)
}

func (t *tap) Handle(ctx context.Context, r slog.Record) error {
	agent, source := "", ""
	var rest []string
	visit := func(a slog.Attr) bool {
		v := a.Value.Resolve()
		switch a.Key {
		case "agent", "sandbox":
			if s := v.String(); strings.HasPrefix(s, "ag_") && agent == "" {
				agent = s
				return true
			}
		case "component":
			source = v.String()
			return true
		}
		rest = append(rest, a.Key+"="+formatValue(v))
		return true
	}
	for _, a := range t.attrs {
		visit(a)
	}
	r.Attrs(visit)
	if agent != "" {
		msg := r.Message
		if len(rest) > 0 {
			msg += " " + strings.Join(rest, " ")
		}
		if source == "" {
			source = sourceOf(r.Message)
		}
		t.log.Add(agent, r.Level, source, msg)
	}
	if t.next.Enabled(ctx, r.Level) {
		return t.next.Handle(ctx, r)
	}
	return nil
}

func (t *tap) WithAttrs(as []slog.Attr) slog.Handler {
	return &tap{next: t.next.WithAttrs(as), log: t.log, attrs: append(append([]slog.Attr(nil), t.attrs...), as...)}
}

func (t *tap) WithGroup(name string) slog.Handler {
	return &tap{next: t.next.WithGroup(name), log: t.log, attrs: t.attrs}
}

// sourceOf guesses an entry's source from its message's prefix.
func sourceOf(msg string) string {
	switch {
	case strings.HasPrefix(msg, "tunnel:"):
		return "tunnel"
	case strings.HasPrefix(msg, "guest"):
		return "guest"
	case strings.HasPrefix(msg, "harness stderr"):
		return "stderr"
	case strings.HasPrefix(msg, "harness"), strings.HasPrefix(msg, "acp"):
		return "harness"
	case strings.Contains(msg, "snapshot"), strings.Contains(msg, "boot"), strings.HasPrefix(msg, "wake"), strings.HasPrefix(msg, "sandbox"), strings.HasPrefix(msg, "machine"):
		return "machine"
	}
	return "server"
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindDuration:
		return v.Duration().Round(time.Millisecond).String()
	case slog.KindString:
		s := v.String()
		if strings.ContainsAny(s, " \t\n\"") {
			return fmt.Sprintf("%q", s)
		}
		return s
	}
	return v.String()
}
