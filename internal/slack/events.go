package slack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

// HandleEvent handles an Events API event (Socket Mode delivers them; tests
// call it directly).
func (s *Service) HandleEvent(ctx context.Context, e slackevents.EventsAPIEvent) {
	if e.Type != slackevents.CallbackEvent || s.api() == nil {
		return
	}
	team := e.TeamID
	if team == "" {
		team = s.installation().teamID
	}
	switch ev := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if ev.BotID != "" || ev.User == "" || strings.HasPrefix(ev.Channel, "D") || ev.Edited != nil {
			return // DMs arrive as message.im; bots and edits are ignored
		}
		s.incoming(ctx, incoming{team: team, channel: ev.Channel, user: ev.User, text: ev.Text, ts: ev.TimeStamp, threadTS: ev.ThreadTimeStamp, mention: true})
	case *slackevents.MessageEvent:
		s.onMessage(ctx, team, ev)
	case *slackevents.AppHomeOpenedEvent:
		if ev.Tab == "home" {
			s.publishHome(ctx, team, ev.User)
		}
	}
}

func (s *Service) onMessage(ctx context.Context, team string, ev *slackevents.MessageEvent) {
	switch ev.SubType {
	case "", "thread_broadcast", "file_share":
	default:
		return // edits, deletions, joins, bot messages
	}
	inst := s.installation()
	if ev.BotID != "" || ev.User == "" || ev.User == inst.botUserID {
		return
	}
	in := incoming{team: team, channel: ev.Channel, user: ev.User, text: ev.Text, ts: ev.TimeStamp, threadTS: ev.ThreadTimeStamp, dm: ev.ChannelType == "im"}
	if in.dm {
		s.incoming(ctx, in)
		return
	}
	// In channels a mention arrives as app_mention too; that one handles it.
	if inst.botUserID != "" && strings.Contains(ev.Text, "<@"+inst.botUserID+">") {
		return
	}
	if in.threadTS == "" || in.threadTS == in.ts {
		return
	}
	if _, err := s.o.Store.GetSlackThread(ctx, db.GetSlackThreadParams{TeamID: team, ChannelID: ev.Channel, ThreadTs: in.threadTS}); err != nil {
		return // not a thread of ours
	}
	s.incoming(ctx, in)
}

// incoming is a message for the bot: a DM, a mention, or a reply in a
// bound thread.
type incoming struct {
	team, channel, user, text, ts, threadTS string
	dm, mention                             bool
}

func (s *Service) incoming(ctx context.Context, in incoming) {
	text := normalize(in.text, s.installation().botUserID)
	threadTS := in.threadTS
	if threadTS == "" {
		threadTS = in.ts
	}
	r := reply{channel: in.channel, threadTS: threadTS, user: in.user}
	uctx, u, ok, err := s.linkedUser(ctx, in.team, in.user)
	if err != nil {
		s.lg.Warn("slack user lookup", "err", err)
		return
	}
	key := threadKey{in.team, in.channel, threadTS}
	if !ok {
		if in.threadTS != "" && in.threadTS != in.ts && !in.mention && !s.linkPromptDue(key, in.user) {
			return // asked in this thread not long ago
		}
		s.askToLink(ctx, r, in.team)
		return
	}
	if in.threadTS != "" && in.threadTS != in.ts {
		if row, err := s.o.Store.GetSlackThread(ctx, db.GetSlackThreadParams{TeamID: in.team, ChannelID: in.channel, ThreadTs: in.threadTS}); err == nil {
			if text != "" {
				s.enqueue(key, row.SessionID, &queued{userID: u.ID, slackUser: in.user, text: text, msgTS: in.ts})
			}
			return
		}
		// A mention (or DM) in a thread that is not bound: bind it.
	}
	if text == "" {
		s.say(ctx, r, "Hi! Tell me what to work on, and I'll start a thread with your agent.")
		return
	}
	agent, agents, err := s.resolveAgent(uctx, in.team, in.channel)
	if err != nil {
		s.tellError(ctx, in.channel, threadTS, in.user, "Could not list your agents", err)
		return
	}
	if agent == nil {
		if len(agents) == 0 {
			s.say(ctx, r, "You have no agents you can start a thread with yet. Create one at "+s.o.PublicURL+"/new.")
			return
		}
		s.offerPicker(ctx, &pick{team: in.team, channel: in.channel, threadTS: threadTS, slackUser: in.user, userID: u.ID, text: text, msgTS: in.ts}, agents)
		return
	}
	s.startChat(ctx, chatStart{team: in.team, channel: in.channel, threadTS: threadTS, slackUser: in.user, userID: u.ID, agent: *agent, text: text, msgTS: in.ts})
}

var (
	slackLink   = regexp.MustCompile(`<((?:https?|mailto):[^|>]+)\|([^>]+)>`)
	slackURL    = regexp.MustCompile(`<((?:https?|mailto):[^>]+)>`)
	slackMarker = regexp.MustCompile(`<!(here|channel|everyone)[^>]*>`)
)

// normalize turns a Slack message into the prompt text: the bot's mention
// goes, links become Markdown, entities are decoded.
func normalize(text, botUserID string) string {
	if botUserID != "" {
		text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	}
	text = slackLink.ReplaceAllString(text, "[$2]($1)")
	text = slackURL.ReplaceAllString(text, "$1")
	text = slackMarker.ReplaceAllString(text, "@$1")
	return strings.TrimSpace(html.UnescapeString(text))
}

// usableAgents are the agents the user can chat with (owned or shared).
func (s *Service) usableAgents(uctx context.Context) ([]gen.Agent, error) {
	res, err := s.o.API.ListAgents(uctx, gen.ListAgentsRequestObject{})
	if err != nil {
		return nil, err
	}
	var out []gen.Agent
	for _, a := range res.(gen.ListAgents200JSONResponse) {
		if a.Role == gen.Owner || a.Role == gen.Shared {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// resolveAgent picks the agent for a new chat: the channel's default, or
// the only agent the user can use. Otherwise it returns the candidates.
func (s *Service) resolveAgent(uctx context.Context, team, channel string) (*gen.Agent, []gen.Agent, error) {
	agents, err := s.usableAgents(uctx)
	if err != nil {
		return nil, nil, err
	}
	if def, err := s.o.Store.GetSlackChannelAgent(uctx, db.GetSlackChannelAgentParams{TeamID: team, ChannelID: channel}); err == nil {
		for i := range agents {
			if agents[i].Id == def.AgentID {
				return &agents[i], agents, nil
			}
		}
	}
	if len(agents) == 1 {
		return &agents[0], agents, nil
	}
	return nil, agents, nil
}

// findAgent finds a usable agent by id or name (case-insensitive).
func findAgent(agents []gen.Agent, ref string) *gen.Agent {
	ref = strings.TrimSpace(ref)
	for i := range agents {
		if agents[i].Id == ref {
			return &agents[i]
		}
	}
	for i := range agents {
		if strings.EqualFold(agents[i].Name, ref) {
			return &agents[i]
		}
	}
	return nil
}

func (s *Service) setDefault(ctx context.Context, team, channel, agentID, userID string) error {
	return s.o.Store.PutSlackChannelAgent(ctx, db.PutSlackChannelAgentParams{TeamID: team, ChannelID: channel, AgentID: agentID, SetBy: userID, UpdatedAt: s.now().Unix()})
}

// pick is a message waiting for its author to choose an agent.
type pick struct {
	team, channel, threadTS, slackUser, userID, text, msgTS string
	at                                                      time.Time
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) offerPicker(ctx context.Context, p *pick, agents []gen.Agent) {
	id := randomID()
	p.at = s.now()
	s.mu.Lock()
	for k, old := range s.picks {
		if p.at.Sub(old.at) > 30*time.Minute {
			delete(s.picks, k)
		}
	}
	s.picks[id] = p
	s.mu.Unlock()
	s.send(ctx, reply{channel: p.channel, threadTS: p.threadTS, user: p.slackUser}, "Which agent should answer?", pickerBlocks(id, agents))
}

func pickerBlocks(id string, agents []gen.Agent) []slackgo.Block {
	var opts []*slackgo.OptionBlockObject
	for _, a := range agents {
		if len(opts) == 100 {
			break
		}
		label := a.Name + " · " + string(a.State)
		if a.Role == gen.Shared {
			label += " · " + a.Owner.Name
		}
		opts = append(opts, slackgo.NewOptionBlockObject(a.Id, plain(truncate(label, 75)), nil))
	}
	sel := slackgo.NewOptionsSelectBlockElement(slackgo.OptTypeStatic, plain("Choose an agent"), "pick", opts...)
	def := slackgo.NewCheckboxGroupsBlockElement("pick_default", slackgo.NewOptionBlockObject("default", plain("Use it as the default here"), nil))
	return []slackgo.Block{
		slackgo.NewSectionBlock(mrkdwn("Which agent should answer? Your message is sent once you choose."), nil, slackgo.NewAccessory(sel), slackgo.SectionBlockOptionBlockID("pick:"+id)),
		slackgo.NewActionBlock("pickdef:"+id, def),
	}
}

// chatStart describes a new chat.
type chatStart struct {
	team, channel string
	// threadTS is the thread to bind; '' posts the header as a new thread.
	threadTS  string
	slackUser string
	userID    string
	agent     gen.Agent
	text      string // the first message ('' for none)
	msgTS     string // the Slack message holding text
}

// startChat creates a session, binds a thread to it and sends the first
// message.
func (s *Service) startChat(ctx context.Context, c chatStart) {
	uctx, err := s.userContext(ctx, c.userID)
	if err != nil {
		return
	}
	r := reply{channel: c.channel, threadTS: c.threadTS, user: c.slackUser}
	res, err := s.o.API.CreateSession(uctx, gen.CreateSessionRequestObject{AgentId: c.agent.Id, Body: &gen.CreateSessionJSONRequestBody{}})
	if err != nil {
		s.say(ctx, r, ":warning: Could not start a thread with *"+escape(c.agent.Name)+"*: "+escape(s.errorText(err))+".")
		return
	}
	sess := gen.Session(res.(gen.CreateSession201JSONResponse))
	blocks, text := s.headerBlocks(uctx, sess, c.agent)
	channel, threadTS, headerTS := c.channel, c.threadTS, ""
	if threadTS == "" {
		ch, ts, err := s.postTo(ctx, c.channel, "", text, blocks)
		if err != nil {
			_, _ = s.o.API.DeleteSession(uctx, gen.DeleteSessionRequestObject{SessionId: sess.Id})
			msg := "I could not post here"
			if e := slackError(err); e == "not_in_channel" || e == "channel_not_found" {
				msg += ": invite me to the channel first (`/invite @agenthold`)"
			}
			s.say(ctx, r, ":warning: "+msg+".")
			return
		}
		channel, threadTS, headerTS = ch, ts, ts
	} else if ts, err := s.post(ctx, channel, threadTS, text, blocks); err == nil {
		headerTS = ts
	}
	key := threadKey{c.team, channel, threadTS}
	if err := s.bind(ctx, key, sess.Id, c.agent.Id, c.userID, headerTS); err != nil {
		s.lg.Warn("bind slack thread", "err", err)
		return
	}
	if c.text != "" && c.msgTS == "" {
		// A slash command's text is not in the channel: show it.
		name := "You"
		if u, err := s.o.Store.GetUser(ctx, c.userID); err == nil {
			name = u.Name
		}
		_, _ = s.post(ctx, channel, threadTS, name+": "+truncate(c.text, 200), []slackgo.Block{sectionBlock("*" + escape(name) + "*: " + escape(truncate(c.text, 2800)))})
	}
	if c.text != "" {
		s.enqueue(key, sess.Id, &queued{userID: c.userID, slackUser: c.slackUser, text: c.text, msgTS: c.msgTS})
	}
}

// bind binds a thread to a session; its worker renders what comes after
// the session's current last event.
func (s *Service) bind(ctx context.Context, k threadKey, sessionID, agentID, userID, headerTS string) error {
	seq, err := s.o.Store.MaxSessionSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := s.o.Store.CreateSlackThread(ctx, db.CreateSlackThreadParams{
		TeamID: k.team, ChannelID: k.channel, ThreadTs: k.ts, SessionID: sessionID, AgentID: agentID,
		CreatedBy: userID, HeaderTs: headerTS, CreatedAt: s.now().Unix(), LastSeq: seq,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.bound[sessionID] = true
	delete(s.keys, sessionID)
	s.mu.Unlock()
	s.withWorker(k, sessionID, nil)
	return nil
}

// linkPromptEvery is how often an unlinked Slack user writing in a thread
// is asked to link their account.
const linkPromptEvery = time.Hour

// linkPromptDue tells whether an unlinked Slack user writing in a thread is
// to be shown the link prompt (again), and notes that they were.
func (s *Service) linkPromptDue(k threadKey, slackUser string) bool {
	now := s.now()
	key := k.String() + "/" + slackUser
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.prompted[key]; ok && now.Sub(at) < linkPromptEvery {
		return false
	}
	if len(s.prompted) >= 1000 {
		for k, at := range s.prompted {
			if now.Sub(at) >= linkPromptEvery {
				delete(s.prompted, k)
			}
		}
	}
	s.prompted[key] = now
	return true
}

// forgetSession drops a deleted session's threads from the caches.
func (s *Service) forgetSession(sessionID string, keys []threadKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bound, sessionID)
	delete(s.keys, sessionID)
	for _, k := range keys {
		delete(s.threads, k)
	}
}
