package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// idleAfter is how long a thread worker without a turn or queued messages
// lives on; the next event of its session starts it again.
var idleAfter = 10 * time.Minute

const (
	// maxText is the agent text one message holds before the answer goes
	// on in a new message (Slack's markdown block takes 12,000 characters).
	maxText = 10000
	// maxTools is how many tool calls a turn's message lists.
	maxTools = 8
)

// thread mirrors one session into one Slack thread and sends the prompts
// written there, one at a time. Its goroutine is the only one rendering the
// thread, so events, queued prompts and message updates stay in order.
type thread struct {
	s         *Service
	key       threadKey
	sessionID string
	wake      chan struct{}

	// Guarded by Service.mu.
	queue []*queued

	// Owned by the worker goroutine.
	row     db.SlackThread
	cursor  int64
	running bool
	turn    *turnView
	perms   map[string]*permView
}

// queued is a message written in the thread, waiting to be sent.
type queued struct {
	userID    string // agenthold user it is sent as
	slackUser string
	text      string
	msgTS     string // the Slack message (for the waiting reaction)
	reacted   bool
}

type turnView struct {
	started time.Time
	status  string
	tools   []*toolView
	plan    []planEntry
	text    string // the current message's part of the answer
	ts      string // the current message ('' until posted)
	dirty   bool
	pushed  time.Time
	ended   bool
	footer  string
}

type toolView struct{ id, title, status string }

type planEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type permView struct {
	ts, title string
	options   []permOption
}

type permOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// worker returns the thread's worker, starting it with the given cursor
// (the last event it must not render) when it is not running.
func (s *Service) worker(k threadKey, sessionID string, cursor int64) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.threads[k]; t != nil {
		return t
	}
	t := &thread{s: s, key: k, sessionID: sessionID, cursor: max(cursor, 0), wake: make(chan struct{}, 1), perms: map[string]*permView{}}
	s.threads[k] = t
	if s.closed {
		return t
	}
	s.wg.Add(1)
	go t.run()
	return t
}

func (t *thread) poke() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// enqueue queues a message of the thread for its session.
func (s *Service) enqueue(ctx context.Context, k threadKey, sessionID string, q *queued) {
	seq, _ := s.o.Store.MaxSessionSeq(ctx, sessionID)
	t := s.worker(k, sessionID, seq)
	s.mu.Lock()
	t.queue = append(t.queue, q)
	s.mu.Unlock()
	t.poke()
}

func (t *thread) run() {
	defer t.s.wg.Done()
	ctx := t.s.ctx
	row, err := t.s.o.Store.GetSlackThread(ctx, db.GetSlackThreadParams{TeamID: t.key.team, ChannelID: t.key.channel, ThreadTs: t.key.ts})
	if err != nil {
		t.s.mu.Lock()
		delete(t.s.threads, t.key)
		t.s.mu.Unlock()
		return
	}
	t.row = row
	idle := time.NewTimer(idleAfter)
	defer idle.Stop()
	var flushC <-chan time.Time
	for {
		t.catchUp(ctx)
		t.sendQueued(ctx)
		if d := t.flush(ctx, false); d > 0 && flushC == nil {
			flushC = time.After(d)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
		case <-flushC:
			flushC = nil
		case <-idle.C:
			if t.tryExit() {
				return
			}
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(idleAfter)
	}
}

func (t *thread) tryExit() bool {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if t.running || t.turn != nil || len(t.queue) > 0 || len(t.wake) > 0 {
		return false
	}
	delete(t.s.threads, t.key)
	return true
}

// catchUp renders the session's events after the cursor.
func (t *thread) catchUp(ctx context.Context) {
	for {
		rows, err := t.s.o.Store.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: t.sessionID, Seq: t.cursor, Limit: 500})
		if err != nil {
			if ctx.Err() == nil {
				t.s.lg.Warn("read session events", "session", t.sessionID, "err", err)
			}
			return
		}
		for _, r := range rows {
			t.apply(ctx, session.Event{Seq: r.Seq, At: time.UnixMilli(r.At).UTC(), Type: r.Type, UserID: r.UserID, Data: json.RawMessage(r.Data)})
			t.cursor = r.Seq
		}
		if len(rows) < 500 {
			return
		}
	}
}

func (t *thread) apply(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EventPrompt:
		t.onPrompt(ctx, ev)
	case session.EventUpdate:
		t.onUpdate(ctx, ev)
	case session.EventPermission:
		t.onPermission(ctx, ev)
	case session.EventPermissionResolved:
		t.onPermissionResolved(ctx, ev)
	case session.EventNotice, session.EventError:
		var d struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		icon := ":information_source:"
		if ev.Type == session.EventError {
			icon = ":warning:"
		}
		t.flush(ctx, true) // the answer so far comes before the note
		_, _ = t.s.post(ctx, t.key.channel, t.key.ts, d.Message, []slackgo.Block{contextBlock(icon + " " + escape(truncate(d.Message, 2900)))})
	case session.EventTurnEnd:
		var d struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		t.finish(ctx, ev.At, d.StopReason)
	}
}

func (t *thread) onPrompt(ctx context.Context, ev session.Event) {
	var d struct {
		Text string `json:"text"`
		User struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Origin map[string]string `json:"origin"`
	}
	_ = json.Unmarshal(ev.Data, &d)
	if t.turn != nil {
		t.finish(ctx, ev.At, "")
	}
	from, fromSlack := t.s.claim(t.sessionID, ev.Seq, d.User.ID, d.Text)
	if !fromSlack || from != t.key {
		name := d.User.Name
		if name == "" {
			name = "Someone"
		}
		label := "*" + escape(name) + "*"
		switch {
		case fromSlack:
			label += " (Slack)"
		case d.Origin == nil && !strings.Contains(name, "("):
			label += " (web)"
		}
		body := label + ": " + escape(truncate(d.Text, 2800))
		_, _ = t.s.post(ctx, t.key.channel, t.key.ts, name+": "+truncate(d.Text, 200), []slackgo.Block{sectionBlock(body)})
	}
	t.running = true
	t.turn = &turnView{started: ev.At, status: t.initialStatus(ctx), dirty: true}
	t.flush(ctx, true)
}

// initialStatus is what a turn shows before the harness answers.
func (t *thread) initialStatus(ctx context.Context) string {
	if sb, err := t.s.o.Store.GetSandbox(ctx, t.row.AgentID); err == nil {
		switch gen.SandboxState(sb.State) {
		case gen.SandboxStateSleeping, gen.SandboxStateStarting, gen.SandboxStateCreating, gen.SandboxStateError:
			return "Waking the agent…"
		}
	}
	return "Thinking…"
}

// current returns the turn being rendered, starting one for events of a
// turn whose prompt came before the thread was bound.
func (t *thread) current(at time.Time) *turnView {
	if t.turn == nil {
		t.turn = &turnView{started: at, status: "Working…"}
		t.running = true
	}
	return t.turn
}

func (t *thread) onUpdate(ctx context.Context, ev session.Event) {
	var u struct {
		Kind       string          `json:"sessionUpdate"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		Title      *string         `json:"title"`
		Status     *string         `json:"status"`
		Entries    []planEntry     `json:"entries"`
	}
	if json.Unmarshal(ev.Data, &u) != nil {
		return
	}
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan":
	default:
		return
	}
	tv := t.current(ev.At)
	switch u.Kind {
	case "agent_message_chunk":
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(u.Content, &c)
		tv.text += c.Text
		tv.status = "Writing…"
		if len(tv.text) > maxText {
			t.split(ctx, tv)
		}
	case "agent_thought_chunk":
		tv.status = "Thinking…"
	case "tool_call", "tool_call_update":
		var tool *toolView
		for _, x := range tv.tools {
			if x.id == u.ToolCallID {
				tool = x
			}
		}
		if tool == nil {
			tool = &toolView{id: u.ToolCallID, title: u.ToolCallID, status: "pending"}
			tv.tools = append(tv.tools, tool)
		}
		if u.Title != nil && *u.Title != "" {
			tool.title = *u.Title
		}
		if u.Status != nil {
			tool.status = *u.Status
		}
		if tool.status == "completed" || tool.status == "failed" {
			tv.status = "Thinking…"
		} else {
			tv.status = tool.title
		}
	case "plan":
		tv.plan = u.Entries
	}
	tv.dirty = true
}

// split ends the current message at a line break near maxText and goes on
// in a new one.
func (t *thread) split(ctx context.Context, tv *turnView) {
	cut := splitPoint(tv.text, maxText)
	head, rest := tv.text[:cut], strings.TrimLeft(tv.text[cut:], "\n")
	blocks := []slackgo.Block{markdownBlock(head)}
	if tv.ts == "" {
		ts, _ := t.s.post(ctx, t.key.channel, t.key.ts, truncate(head, 150), blocks)
		_ = ts
	} else {
		_ = t.s.update(ctx, t.key.channel, tv.ts, truncate(head, 150), blocks)
	}
	tv.text, tv.ts, tv.pushed, tv.dirty = rest, "", time.Time{}, true
}

func splitPoint(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	window := s[:limit]
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i > limit/2 {
			return i + len(sep)
		}
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return cut
}

func (t *thread) onPermission(ctx context.Context, ev session.Event) {
	var d struct {
		RequestID string `json:"requestId"`
		ToolCall  struct {
			Title      string `json:"title"`
			ToolCallID string `json:"toolCallId"`
		} `json:"toolCall"`
		Options []permOption `json:"options"`
		Policy  string       `json:"policy"`
	}
	if json.Unmarshal(ev.Data, &d) != nil || d.Policy != session.PolicyAsk {
		return // auto-approved: the tool call shows it
	}
	title := d.ToolCall.Title
	if title == "" {
		title = "Permission requested"
	}
	tv := t.current(ev.At)
	tv.status, tv.dirty = "Waiting for approval", true
	t.flush(ctx, true)
	buttons := []slackgo.BlockElement{}
	for i, o := range d.Options {
		v, _ := json.Marshal(permValue{Session: t.sessionID, Request: d.RequestID, Option: o.OptionID})
		b := slackgo.NewButtonBlockElement(fmt.Sprintf("perm_%d", i), string(v), plain(truncate(o.Name, 70)))
		switch {
		case strings.HasPrefix(o.Kind, "allow"):
			b.Style = slackgo.StylePrimary
		case strings.HasPrefix(o.Kind, "reject"):
			b.Style = slackgo.StyleDanger
		}
		buttons = append(buttons, b)
		if len(buttons) == 25 {
			break
		}
	}
	blocks := []slackgo.Block{sectionBlock(":lock: *Permission requested*\n" + escape(truncate(title, 2800)))}
	if len(buttons) > 0 {
		blocks = append(blocks, slackgo.NewActionBlock("perm", buttons...))
	}
	ts, err := t.s.post(ctx, t.key.channel, t.key.ts, "Permission requested: "+truncate(title, 200), blocks)
	if err == nil {
		t.perms[d.RequestID] = &permView{ts: ts, title: title, options: d.Options}
	}
}

type permValue struct {
	Session string `json:"s"`
	Request string `json:"r"`
	Option  string `json:"o"`
}

func (t *thread) onPermissionResolved(ctx context.Context, ev session.Event) {
	var d struct {
		RequestID string `json:"requestId"`
		OptionID  string `json:"optionId"`
		By        string `json:"by"`
	}
	_ = json.Unmarshal(ev.Data, &d)
	p := t.perms[d.RequestID]
	if p == nil {
		return
	}
	delete(t.perms, d.RequestID)
	by := "someone"
	if u, err := t.s.o.Store.GetUser(ctx, d.By); err == nil && u.Name != "" {
		by = u.Name
	}
	verb := "Answered"
	for _, o := range p.options {
		if o.OptionID != d.OptionID {
			continue
		}
		switch {
		case strings.HasPrefix(o.Kind, "allow"):
			verb = ":white_check_mark: Allowed"
		case strings.HasPrefix(o.Kind, "reject"):
			verb = ":no_entry_sign: Rejected"
		default:
			verb = escape(o.Name)
		}
	}
	text := verb + " by *" + escape(by) + "*: " + escape(truncate(p.title, 2800))
	_ = t.s.update(ctx, t.key.channel, p.ts, verb+" by "+by, []slackgo.Block{sectionBlock(text)})
	if t.turn != nil {
		t.turn.status, t.turn.dirty = "Thinking…", true
	}
}

// finish ends the rendered turn (reason "" when a new prompt replaced it).
func (t *thread) finish(ctx context.Context, at time.Time, reason string) {
	tv := t.turn
	if tv != nil {
		d := at.Sub(tv.started)
		tv.ended, tv.dirty = true, true
		tv.footer = footer(reason, d)
		t.flush(ctx, true)
	}
	for id, p := range t.perms {
		_ = t.s.update(ctx, t.key.channel, p.ts, "Permission request ended", []slackgo.Block{
			sectionBlock(":lock: " + escape(truncate(p.title, 2800)) + "\n_The turn ended before it was answered._"),
		})
		delete(t.perms, id)
	}
	t.turn, t.running = nil, false
	if reason != "" {
		t.s.refreshHeader(ctx, t.row)
	}
}

func footer(reason string, d time.Duration) string {
	took := formatDuration(d)
	switch reason {
	case "end_turn", "":
		return ":white_check_mark: Done · " + took
	case "cancelled":
		return ":black_square_for_stop: Cancelled · " + took
	case "error":
		return ":warning: Failed · " + took
	default:
		return "Stopped (" + escape(reason) + ") · " + took
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// flush pushes the turn's message when it changed and the throttle allows
// (force skips the throttle). It returns how long to wait otherwise.
func (t *thread) flush(ctx context.Context, force bool) time.Duration {
	tv := t.turn
	if tv == nil || !tv.dirty {
		return 0
	}
	if !force && tv.ts != "" {
		if wait := t.s.o.Throttle - time.Since(tv.pushed); wait > 0 {
			return wait
		}
	}
	blocks, text := t.render(tv)
	if tv.ts == "" {
		ts, err := t.s.post(ctx, t.key.channel, t.key.ts, text, blocks)
		if err != nil {
			tv.dirty = false
			return 0
		}
		tv.ts = ts
	} else if err := t.s.update(ctx, t.key.channel, tv.ts, text, blocks); err != nil {
		tv.dirty = false
		return 0
	}
	tv.pushed, tv.dirty = time.Now(), false
	return 0
}

func (t *thread) render(tv *turnView) ([]slackgo.Block, string) {
	var blocks []slackgo.Block
	if n := len(tv.tools); n > 0 {
		var lines []string
		shown := tv.tools
		if n > maxTools {
			lines = append(lines, fmt.Sprintf("_+%d more_", n-maxTools))
			shown = tv.tools[n-maxTools:]
		}
		for _, x := range shown {
			lines = append(lines, toolIcon(x.status)+" "+escape(truncate(oneLine(x.title), 120)))
		}
		blocks = append(blocks, contextBlock(strings.Join(lines, "\n")))
	}
	if len(tv.plan) > 0 {
		lines := []string{"*Plan*"}
		for i, e := range tv.plan {
			if i == 20 {
				lines = append(lines, fmt.Sprintf("_+%d more_", len(tv.plan)-20))
				break
			}
			lines = append(lines, planIcon(e.Status)+" "+escape(truncate(oneLine(e.Content), 140)))
		}
		blocks = append(blocks, sectionBlock(strings.Join(lines, "\n")))
	}
	if strings.TrimSpace(tv.text) != "" {
		blocks = append(blocks, markdownBlock(tv.text))
	}
	fallback := truncate(strings.TrimSpace(tv.text), 150)
	if tv.ended {
		blocks = append(blocks, contextBlock(tv.footer))
		if fallback == "" {
			fallback = tv.footer
		}
	} else {
		blocks = append(blocks,
			contextBlock("_"+escape(truncate(oneLine(tv.status), 200))+"_"),
			slackgo.NewActionBlock("turn", stopButton(t.sessionID)))
		if fallback == "" {
			fallback = tv.status
		}
	}
	return blocks, fallback
}

func stopButton(sessionID string) *slackgo.ButtonBlockElement {
	b := slackgo.NewButtonBlockElement("stop", sessionID, plain("Stop"))
	b.Style = slackgo.StyleDanger
	return b
}

func toolIcon(status string) string {
	switch status {
	case "completed":
		return ":white_check_mark:"
	case "failed":
		return ":x:"
	case "in_progress":
		return ":arrows_counterclockwise:"
	}
	return ":hourglass_flowing_sand:"
}

func planIcon(status string) string {
	switch status {
	case "completed":
		return ":ballot_box_with_check:"
	case "in_progress":
		return ":arrow_forward:"
	}
	return ":white_large_square:"
}

// sendQueued sends the first queued message once no turn runs; messages
// that have to wait get a reaction until they are sent.
func (t *thread) sendQueued(ctx context.Context) {
	for {
		t.s.mu.Lock()
		if len(t.queue) == 0 {
			t.s.mu.Unlock()
			return
		}
		q := t.queue[0]
		t.s.mu.Unlock()
		if t.running || t.s.o.Sessions.Running(t.sessionID) {
			t.markWaiting(ctx)
			return
		}
		err := t.s.promptAs(ctx, t, q)
		if busy(err) {
			t.markWaiting(ctx)
			return
		}
		t.s.mu.Lock()
		t.queue = t.queue[1:]
		t.s.mu.Unlock()
		if q.reacted {
			_ = t.s.unreact(ctx, t.key.channel, q.msgTS)
		}
		if err != nil {
			t.s.tellError(ctx, t.key.channel, t.key.ts, q.slackUser, "Your message was not sent", err)
			continue
		}
		t.running = true
	}
}

func (t *thread) markWaiting(ctx context.Context) {
	t.s.mu.Lock()
	var todo []*queued
	for _, q := range t.queue {
		if !q.reacted && q.msgTS != "" {
			q.reacted = true
			todo = append(todo, q)
		}
	}
	t.s.mu.Unlock()
	for _, q := range todo {
		_ = t.s.react(ctx, t.key.channel, q.msgTS)
	}
}

func busy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, session.ErrBusy) {
		return true
	}
	status, _ := api.ErrorStatus(err)
	return status == http.StatusConflict
}

// promptAs sends a queued message as its author.
func (s *Service) promptAs(ctx context.Context, t *thread, q *queued) error {
	uctx, err := s.userContext(ctx, q.userID)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(q.text)
	sent := sentPrompt{sessionID: t.sessionID, userID: q.userID, text: text, from: t.key, at: s.now()}
	s.mu.Lock()
	s.sent = append(s.sent, sent)
	s.mu.Unlock()
	_, err = s.o.API.PromptSession(uctx, gen.PromptSessionRequestObject{SessionId: t.sessionID, Body: &gen.PromptSessionJSONRequestBody{Text: text}})
	if err != nil {
		s.mu.Lock()
		for i, p := range s.sent {
			if p == sent {
				s.sent = append(s.sent[:i], s.sent[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
	return err
}

// claim tells whether a prompt event is one Slack sent, and from which
// thread. Every thread of the session gets the same answer.
func (s *Service) claim(sessionID string, seq int64, userID, text string) (threadKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := claimKey{sessionID, seq}
	if k, ok := s.claims[key]; ok {
		return k, k != threadKey{}
	}
	if len(s.claims) > 1000 {
		s.claims = map[claimKey]threadKey{}
	}
	cutoff := s.now().Add(-5 * time.Minute)
	kept := s.sent[:0]
	var from threadKey
	found := false
	for _, p := range s.sent {
		switch {
		case !found && p.sessionID == sessionID && p.userID == userID && p.text == strings.TrimSpace(text):
			from, found = p.from, true
		case p.at.After(cutoff):
			kept = append(kept, p)
		}
	}
	s.sent = kept
	s.claims[key] = from
	return from, found
}
