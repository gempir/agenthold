// Package idle puts agents to sleep when nobody uses them. An agent is idle
// when it has no running turn, no open terminal and no activity (prompts,
// terminal or file use, CPU used by processes in its sandbox) for its idle
// timeout. Sleeping stops the sandbox; the disk and conversations survive and
// any prompt, terminal or other request wakes it again.
package idle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Tracker records the last activity per agent.
type Tracker struct {
	mu        sync.Mutex
	now       func() time.Time
	last      map[string]time.Time
	terminals map[string]int
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{now: time.Now, last: map[string]time.Time{}, terminals: map[string]int{}}
}

// Touch records activity now.
func (t *Tracker) Touch(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[agentID] = t.now()
}

// TerminalOpened keeps the agent awake until TerminalClosed.
func (t *Tracker) TerminalOpened(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.terminals[agentID]++
	t.last[agentID] = t.now()
}

// TerminalClosed ends a TerminalOpened.
func (t *Tracker) TerminalClosed(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminals[agentID] > 0 {
		t.terminals[agentID]--
	}
	t.last[agentID] = t.now()
}

// Terminals returns the number of open terminals.
func (t *Tracker) Terminals(agentID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminals[agentID]
}

// Last returns the last recorded activity (zero if none).
func (t *Tracker) Last(agentID string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last[agentID]
}

func (t *Tracker) forget(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, agentID)
}

// Busy reports whether an agent has a running turn.
type Busy func(agentID string) bool

// Sessions provides the tunnel session of a connected sandbox.
type Sessions interface {
	Session(agentID string) (*tunnel.Session, bool)
}

// CPUThreshold is the share of one core above which a sandbox counts as busy.
const CPUThreshold = 0.05

// Sleeper stops idle sandboxes.
type Sleeper struct {
	Store     *store.Store
	Sandboxes *sandbox.Manager
	Tracker   *Tracker
	Busy      Busy
	Tunnel    Sessions
	Log       *slog.Logger
	// Interval between checks. Default 30s.
	Interval time.Duration
	Now      func() time.Time

	cpu map[string]cpuReading
}

type cpuReading struct {
	ticks uint64
	at    time.Time
}

// Run checks periodically until ctx ends.
func (s *Sleeper) Run(ctx context.Context) {
	iv := s.Interval
	if iv == 0 {
		iv = 30 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil {
				s.logger().Warn("idle check failed", "err", err)
			}
		}
	}
}

func (s *Sleeper) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Sleeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Tick checks every running sandbox once and stops the idle ones.
func (s *Sleeper) Tick(ctx context.Context) error {
	if s.cpu == nil {
		s.cpu = map[string]cpuReading{}
	}
	sbs, err := s.Store.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, sb := range sbs {
		if sandbox.Phase(sb.State) != sandbox.PhaseRunning {
			delete(s.cpu, sb.AgentID)
			s.Tracker.forget(sb.AgentID)
			continue
		}
		id := sb.AgentID
		if s.Busy != nil && s.Busy(id) || s.Tracker.Terminals(id) > 0 || s.cpuBusy(ctx, id, now) {
			s.Tracker.Touch(id)
			continue
		}
		last := s.Tracker.Last(id)
		if last.IsZero() {
			// Unknown (for example after a server restart): start counting now.
			s.Tracker.Touch(id)
			continue
		}
		a, err := s.Store.GetAgent(ctx, id)
		if err != nil {
			continue
		}
		timeout := time.Duration(a.IdleMinutes) * time.Minute
		if now.Sub(last) < timeout {
			continue
		}
		s.logger().Info("agent idle, putting it to sleep", "agent", id, "idle_for", now.Sub(last).Round(time.Second))
		if err := s.Sandboxes.Stop(ctx, id); err != nil && !errors.Is(err, sandbox.ErrConflict) {
			s.logger().Warn("sleep", "agent", id, "err", err)
		}
		delete(s.cpu, id)
	}
	return nil
}

// cpuBusy compares the sandbox's CPU time with the previous reading.
func (s *Sleeper) cpuBusy(ctx context.Context, agentID string, now time.Time) bool {
	if s.Tunnel == nil {
		return false
	}
	ts, ok := s.Tunnel.Session(agentID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := ts.Stats(ctx)
	if err != nil {
		return false
	}
	prev, had := s.cpu[agentID]
	s.cpu[agentID] = cpuReading{ticks: st.CPUTicks, at: now}
	if !had || st.CPUTicks < prev.ticks {
		return false
	}
	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return false
	}
	used := float64(st.CPUTicks-prev.ticks) / 100 / elapsed // USER_HZ is 100
	return used >= CPUThreshold
}

// SleepsAt estimates when an agent will sleep if nothing happens, for the UI.
// It returns zero while the agent is busy or not running.
func (s *Sleeper) SleepsAt(agentID string, idleMinutes int64) time.Time {
	if s.Busy != nil && s.Busy(agentID) || s.Tracker.Terminals(agentID) > 0 {
		return time.Time{}
	}
	last := s.Tracker.Last(agentID)
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(time.Duration(idleMinutes) * time.Minute)
}

// SetClock replaces the tracker's clock (tests).
func (t *Tracker) SetClock(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}
