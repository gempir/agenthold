package slack

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api/gen"
)

const helpText = "*agenthold in Slack*\n" +
	"• Message me here, or mention me in a channel: a new chat starts in a thread. Replies in the thread continue it.\n" +
	"• `/agenthold agents`: your agents and their state\n" +
	"• `/agenthold use <agent>`: the default agent of this channel or DM\n" +
	"• `/agenthold new [agent] [message]`: start a chat\n" +
	"• `/agenthold chats [agent]`: recent chats, to continue here\n" +
	"• `/agenthold link` / `unlink`: connect or disconnect your agenthold account"

// HandleCommand handles the /agenthold slash command. Answers go to the
// command's response URL, visible to its user only.
func (s *Service) HandleCommand(ctx context.Context, cmd slackgo.SlashCommand) {
	if s.api() == nil {
		return
	}
	r := reply{responseURL: cmd.ResponseURL, channel: cmd.ChannelID, user: cmd.UserID}
	team := cmd.TeamID
	fields := strings.Fields(cmd.Text)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cmd.Text), firstOr(fields)))
	if sub == "" || sub == "help" {
		s.say(ctx, r, helpText)
		return
	}
	uctx, u, ok, err := s.linkedUser(ctx, team, cmd.UserID)
	if err != nil {
		s.lg.Warn("slack user lookup", "err", err)
		return
	}
	if sub == "unlink" {
		if !ok {
			s.say(ctx, r, "Your Slack account is not connected to agenthold.")
			return
		}
		if err := s.Unlink(ctx, u.ID, team, cmd.UserID); err != nil {
			s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
			return
		}
		s.say(ctx, r, "Disconnected. Your Slack account no longer acts as *"+escape(u.Name)+"* in agenthold.")
		return
	}
	if sub == "link" && ok {
		s.say(ctx, r, "Your Slack account is connected to agenthold as *"+escape(u.Name)+"*. `/agenthold unlink` disconnects it.")
		return
	}
	if !ok {
		s.askToLink(ctx, r, team)
		return
	}
	switch sub {
	case "agents":
		s.cmdAgents(ctx, uctx, r)
	case "use":
		s.cmdUse(ctx, uctx, u.ID, team, r, rest)
	case "new":
		s.cmdNew(ctx, uctx, u.ID, team, r, rest)
	case "chats":
		s.cmdChats(ctx, uctx, r, rest)
	default:
		s.say(ctx, r, "I don't know `"+escape(sub)+"`.\n\n"+helpText)
	}
}

func firstOr(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (s *Service) cmdAgents(ctx, uctx context.Context, r reply) {
	agents, err := s.usableAgents(uctx)
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	if len(agents) == 0 {
		s.say(ctx, r, "You have no agents you can chat with yet. Create one at "+s.o.PublicURL+"/new.")
		return
	}
	blocks := []slackgo.Block{sectionBlock("*Your agents*")}
	for i, a := range agents {
		if i == 20 {
			blocks = append(blocks, contextBlock(fmt.Sprintf("+%d more", len(agents)-20)))
			break
		}
		blocks = append(blocks, agentBlocks(a, true)...)
	}
	s.send(ctx, r, "Your agents", blocks)
}

// agentBlocks show an agent with buttons to start a chat (and to make it
// the channel's default).
func agentBlocks(a gen.Agent, useHere bool) []slackgo.Block {
	line := stateEmoji(a.State) + " *" + escape(a.Name) + "* · " + string(a.State)
	if a.Role == gen.Shared {
		line += " · shared by " + escape(a.Owner.Name)
	}
	buttons := []slackgo.BlockElement{slackgo.NewButtonBlockElement("start_chat", a.Id, plain("Start chat"))}
	if useHere {
		buttons = append(buttons, slackgo.NewButtonBlockElement("use_here", a.Id, plain("Use here")))
	}
	return []slackgo.Block{sectionBlock(line), slackgo.NewActionBlock("agent:"+a.Id, buttons...)}
}

func (s *Service) cmdUse(ctx, uctx context.Context, userID, team string, r reply, ref string) {
	if ref == "" {
		s.say(ctx, r, "Which agent? `/agenthold use <agent>` (a name or an id).")
		return
	}
	agents, err := s.usableAgents(uctx)
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	a := findAgent(agents, ref)
	if a == nil {
		s.say(ctx, r, "You have no agent called *"+escape(ref)+"*. `/agenthold agents` lists yours.")
		return
	}
	if err := s.setDefault(ctx, team, r.channel, a.Id, userID); err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	s.say(ctx, r, "*"+escape(a.Name)+"* now answers new chats here.")
}

func (s *Service) cmdNew(ctx, uctx context.Context, userID, team string, r reply, rest string) {
	agents, err := s.usableAgents(uctx)
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	text := rest
	var agent *gen.Agent
	if f := strings.Fields(rest); len(f) > 0 {
		if a := findAgent(agents, f[0]); a != nil {
			agent, text = a, strings.TrimSpace(strings.TrimPrefix(rest, f[0]))
		}
	}
	if agent == nil {
		agent, _, err = s.resolveAgent(uctx, team, r.channel)
		if err != nil {
			s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
			return
		}
	}
	if agent == nil {
		if len(agents) == 0 {
			s.say(ctx, r, "You have no agents you can chat with yet. Create one at "+s.o.PublicURL+"/new.")
			return
		}
		blocks := []slackgo.Block{sectionBlock("Which agent? Start a chat with one of yours, or `/agenthold new <agent> <message>`.")}
		for i, a := range agents {
			if i == 20 {
				break
			}
			blocks = append(blocks, agentBlocks(a, true)...)
		}
		s.send(ctx, r, "Which agent?", blocks)
		return
	}
	s.startChat(ctx, chatStart{team: team, channel: r.channel, slackUser: r.user, userID: userID, agent: *agent, text: normalize(text, "")})
}

func (s *Service) cmdChats(ctx, uctx context.Context, r reply, ref string) {
	agents, err := s.usableAgents(uctx)
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	if ref != "" {
		a := findAgent(agents, ref)
		if a == nil {
			s.say(ctx, r, "You have no agent called *"+escape(ref)+"*.")
			return
		}
		agents = []gen.Agent{*a}
	}
	chats := s.recentChats(uctx, agents, 10)
	if len(chats) == 0 {
		s.say(ctx, r, "No chats yet.")
		return
	}
	blocks := []slackgo.Block{sectionBlock("*Recent chats*")}
	for _, c := range chats {
		blocks = append(blocks, chatBlocks(s, c)...)
	}
	s.send(ctx, r, "Recent chats", blocks)
}

type chat struct {
	session gen.Session
	agent   gen.Agent
}

// recentChats are the most recently updated sessions of the agents.
func (s *Service) recentChats(uctx context.Context, agents []gen.Agent, n int) []chat {
	var out []chat
	for i, a := range agents {
		if i == 30 {
			break
		}
		res, err := s.o.API.ListSessions(uctx, gen.ListSessionsRequestObject{AgentId: a.Id})
		if err != nil {
			continue
		}
		for _, sess := range res.(gen.ListSessions200JSONResponse) {
			out = append(out, chat{session: sess, agent: a})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].session.UpdatedAt.After(out[j].session.UpdatedAt) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func chatBlocks(s *Service, c chat) []slackgo.Block {
	line := "*" + escape(truncate(c.session.Title, 150)) + "* · " + escape(c.agent.Name) +
		fmt.Sprintf(" · <!date^%d^{date_short_pretty} {time}|%s>", c.session.UpdatedAt.Unix(), c.session.UpdatedAt.Format(time.RFC822))
	if c.session.Running {
		line += " · :arrows_counterclockwise: running"
	}
	return []slackgo.Block{
		sectionBlock(line),
		slackgo.NewActionBlock("chat:"+c.session.Id,
			slackgo.NewButtonBlockElement("continue", c.session.Id, plain("Continue in Slack")),
			linkButton("open_web", "Open in web", s.chatURL(c.agent.Id, c.session.Id)),
		),
	}
}

// continueChat posts a new thread bound to an existing session, with the
// chat's last answer.
func (s *Service) continueChat(ctx context.Context, team, channel, userID, sessionID string, r reply) {
	uctx, err := s.userContext(ctx, userID)
	if err != nil {
		return
	}
	sres, err := s.o.API.GetSession(uctx, gen.GetSessionRequestObject{SessionId: sessionID})
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	sess := gen.Session(sres.(gen.GetSession200JSONResponse))
	ares, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: sess.AgentId})
	if err != nil {
		s.say(ctx, r, ":warning: "+escape(s.errorText(err)))
		return
	}
	agent := gen.Agent(ares.(gen.GetAgent200JSONResponse))
	blocks, text := s.headerBlocks(uctx, sess, agent)
	ch, ts, err := s.postTo(ctx, channel, "", text, blocks)
	if err != nil {
		msg := "I could not post here"
		if e := slackError(err); e == "not_in_channel" || e == "channel_not_found" {
			msg += ": invite me to the channel first (`/invite @agenthold`)"
		}
		s.say(ctx, r, ":warning: "+msg+".")
		return
	}
	if answer := s.lastAnswer(uctx, sessionID); answer != "" {
		if len(answer) > maxText {
			answer = "…" + tail(answer, maxText)
		}
		_, _ = s.post(ctx, ch, ts, truncate(answer, 150), []slackgo.Block{contextBlock("Last answer:"), markdownBlock(answer)})
	}
	if err := s.bind(ctx, threadKey{team, ch, ts}, sessionID, agent.Id, userID, ts); err != nil {
		s.lg.Warn("bind slack thread", "err", err)
	}
}

// lastAnswer is the agent's final message of the session's last turn.
func (s *Service) lastAnswer(uctx context.Context, sessionID string) string {
	var answer strings.Builder
	var after int64
	limit := 5000
	for {
		res, err := s.o.API.ListSessionEvents(uctx, gen.ListSessionEventsRequestObject{SessionId: sessionID, Params: gen.ListSessionEventsParams{After: &after, Limit: &limit}})
		if err != nil {
			return ""
		}
		evs := res.(gen.ListSessionEvents200JSONResponse)
		for _, ev := range evs {
			after = ev.Seq
			switch ev.Type {
			case gen.SessionEventTypePrompt:
				answer.Reset()
			case gen.SessionEventTypeUpdate:
				switch ev.Data["sessionUpdate"] {
				case "agent_message_chunk":
					if c, ok := ev.Data["content"].(map[string]any); ok {
						t, _ := c["text"].(string)
						answer.WriteString(t)
					}
				case "tool_call":
					answer.Reset()
				}
			}
		}
		if len(evs) < limit {
			return strings.TrimSpace(answer.String())
		}
	}
}

// dmChannel opens the app's DM with a Slack user.
func (s *Service) dmChannel(ctx context.Context, slackUser string) (string, error) {
	c := s.api()
	if c == nil {
		return "", errNotConfigured
	}
	ch, _, _, err := c.OpenConversationContext(ctx, &slackgo.OpenConversationParameters{Users: []string{slackUser}})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}
