package slack

import (
	"context"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/gempir/agenthold/internal/session"
)

// runSocket keeps a Socket Mode connection open until ctx ends.
func (s *Service) runSocket(ctx context.Context, client *slackgo.Client) {
	backoff := time.Second
	for ctx.Err() == nil {
		sm := socketmode.New(client)
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- sm.RunContext(runCtx) }()
		up := false
	loop:
		for {
			select {
			case ev := <-sm.Events:
				if ev.Type == socketmode.EventTypeConnected {
					up, backoff = true, time.Second
				}
				s.onSocket(sm, ev)
			case err := <-done:
				msg := "the connection closed"
				if err != nil {
					msg = slackError(err)
				}
				s.setStatus(false, msg)
				break loop
			case <-ctx.Done():
				stop()
				<-done
				s.setStatus(false, "")
				return
			}
		}
		stop()
		if !up {
			backoff = min(2*backoff, time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// onSocket handles one Socket Mode event. Requests from Slack are
// acknowledged at once and handled in the background; only a modal
// submission is answered after handling, since its answer can carry errors.
func (s *Service) onSocket(sm *socketmode.Client, ev socketmode.Event) {
	switch ev.Type {
	case socketmode.EventTypeConnected:
		s.setStatus(true, "")
	case socketmode.EventTypeConnectionError:
		s.setStatus(false, "could not connect to Slack")
	case socketmode.EventTypeInvalidAuth:
		s.setStatus(false, "Slack refused the app-level token (invalid_auth)")
	case socketmode.EventTypeDisconnect:
		s.setStatus(false, "")
	case socketmode.EventTypeEventsAPI:
		if ev.Request != nil {
			_ = sm.Ack(*ev.Request)
		}
		if e, ok := ev.Data.(slackevents.EventsAPIEvent); ok {
			s.goTracked(func() { s.HandleEvent(s.ctx, e) })
		}
	case socketmode.EventTypeSlashCommand:
		if ev.Request != nil {
			_ = sm.Ack(*ev.Request)
		}
		if cmd, ok := ev.Data.(slackgo.SlashCommand); ok {
			s.goTracked(func() { s.HandleCommand(s.ctx, cmd) })
		}
	case socketmode.EventTypeInteractive:
		cb, ok := ev.Data.(slackgo.InteractionCallback)
		if !ok {
			if ev.Request != nil {
				_ = sm.Ack(*ev.Request)
			}
			return
		}
		if cb.Type == slackgo.InteractionTypeViewSubmission {
			req := ev.Request
			s.goTracked(func() {
				payload := s.HandleInteraction(s.ctx, cb)
				if req == nil {
					return
				}
				if payload != nil {
					_ = sm.Ack(*req, payload)
				} else {
					_ = sm.Ack(*req)
				}
			})
			return
		}
		if ev.Request != nil {
			_ = sm.Ack(*ev.Request)
		}
		s.goTracked(func() { s.HandleInteraction(s.ctx, cb) })
	}
}

// observe is the session log observer: it must never block.
func (s *Service) observe(sessionID string, ev session.Event) {
	if s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	bound := s.bound[sessionID] && s.client != nil
	s.mu.Unlock()
	if !bound {
		return
	}
	select {
	case s.events <- sessionID:
	default:
		select {
		case s.overflow <- struct{}{}:
		default:
		}
	}
}

// dispatch wakes the workers of the threads bound to sessions with new
// events, starting them as needed.
func (s *Service) dispatch() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.events:
			s.pokeSession(id)
		case <-s.overflow:
			// Events were dropped: every thread behind its session's log
			// catches up from its persisted cursor.
			s.resync(s.ctx)
		}
	}
}

// resync wakes the worker of every bound thread that has events it has not
// rendered or a turn it left open, starting it as needed.
func (s *Service) resync(ctx context.Context) {
	rows, err := s.o.Store.ListSlackThreads(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.lg.Warn("list slack threads", "err", err)
		}
		return
	}
	last := map[string]int64{}
	for _, r := range rows {
		seq, ok := last[r.SessionID]
		if !ok {
			if seq, err = s.o.Store.MaxSessionSeq(ctx, r.SessionID); err != nil {
				continue
			}
			last[r.SessionID] = seq
		}
		if seq > r.LastSeq || r.TurnSeq > 0 {
			s.withWorker(threadKey{r.TeamID, r.ChannelID, r.ThreadTs}, r.SessionID, (*thread).poke)
		}
	}
}

func (s *Service) pokeSession(sessionID string) {
	s.mu.Lock()
	keys, ok := s.keys[sessionID]
	s.mu.Unlock()
	if !ok {
		rows, err := s.o.Store.ListSlackThreadsOfSession(s.ctx, sessionID)
		if err != nil {
			return
		}
		keys = keys[:0]
		for _, r := range rows {
			keys = append(keys, threadKey{r.TeamID, r.ChannelID, r.ThreadTs})
		}
		s.mu.Lock()
		s.keys[sessionID] = keys
		if len(keys) == 0 {
			delete(s.bound, sessionID)
		}
		s.mu.Unlock()
	}
	for _, k := range keys {
		s.withWorker(k, sessionID, (*thread).poke)
	}
}
