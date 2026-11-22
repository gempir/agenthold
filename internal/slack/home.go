package slack

import (
	"context"
	"net/url"

	slackgo "github.com/slack-go/slack"
)

// publishHome draws a user's App Home tab: their agents and recent chats,
// or the link prompt.
func (s *Service) publishHome(ctx context.Context, team, slackUser string) {
	c := s.api()
	if c == nil || slackUser == "" {
		return
	}
	blocks := []slackgo.Block{slackgo.NewHeaderBlock(plain("agenthold"))}
	uctx, u, ok, err := s.linkedUser(ctx, team, slackUser)
	switch {
	case err != nil:
		return
	case !ok:
		link, err := s.linkBlocks(ctx, team, slackUser)
		if err != nil {
			return
		}
		blocks = append(blocks, link...)
	default:
		blocks = append(blocks, contextBlock("Connected as *"+escape(u.Name)+"* · <"+s.o.PublicURL+"|open agenthold>"))
		agents, err := s.usableAgents(uctx)
		if err != nil {
			blocks = append(blocks, sectionBlock(":warning: "+escape(s.errorText(err))))
			break
		}
		blocks = append(blocks, sectionBlock("*Your agents*"))
		if len(agents) == 0 {
			blocks = append(blocks, sectionBlock("No agents yet. <"+s.o.PublicURL+"/new|Create one in agenthold>."))
		}
		for i, a := range agents {
			if i == 15 {
				break
			}
			blocks = append(blocks, agentBlocks(a, false)...)
		}
		chats := s.recentChats(uctx, agents, 10)
		if len(chats) > 0 {
			blocks = append(blocks, slackgo.NewDividerBlock(), sectionBlock("*Recent threads*"))
			for _, ch := range chats {
				blocks = append(blocks, chatBlocks(s, ch)...)
			}
		}
		blocks = append(blocks, slackgo.NewDividerBlock(), contextBlock("Message me in the Messages tab, or mention me in a channel. `/agenthold help` lists the commands."))
	}
	view := slackgo.HomeTabViewRequest{Type: slackgo.VTHomeTab, Blocks: slackgo.Blocks{BlockSet: blocks}}
	if _, err := c.PublishViewContext(ctx, slackgo.PublishViewContextRequest{UserID: slackUser, View: view}); err != nil && ctx.Err() == nil {
		s.lg.Warn("publish home", "err", slackError(err))
	}
}

// Scopes and events the app needs; the manifest lists them.
var (
	botScopes = []string{
		"app_mentions:read", "chat:write", "commands", "im:history", "im:read", "im:write",
		"channels:history", "groups:history", "mpim:history", "reactions:write", "users:read",
	}
	botEvents = []string{"app_mention", "message.im", "message.channels", "message.groups", "message.mpim", "app_home_opened"}
)

// Manifest implements api.Slack: the Slack app manifest for this server.
func (s *Service) Manifest() map[string]any {
	host := s.o.PublicURL
	if u, err := url.Parse(s.o.PublicURL); err == nil && u.Host != "" {
		host = u.Host
	}
	desc := "Chat with your agenthold agents"
	if host != "" {
		desc += " on " + host
	}
	return map[string]any{
		"display_information": map[string]any{
			"name":             "agenthold",
			"description":      desc,
			"long_description": "Chat with your agenthold agents from Slack: message the app or mention it in a channel to start a thread. Every action runs as your linked agenthold account, with its access. Answers stream into the thread, with tool calls, plans and permission requests.",
			"background_color": "#18181b",
		},
		"features": map[string]any{
			"app_home": map[string]any{
				"home_tab_enabled": true, "messages_tab_enabled": true, "messages_tab_read_only_enabled": false,
			},
			"bot_user": map[string]any{"display_name": "agenthold", "always_online": true},
			"slash_commands": []any{map[string]any{
				"command": "/agenthold", "description": "Chat with your agenthold agents",
				"usage_hint": "help | agents | use <agent> | new [agent] [message] | threads [agent] | link | unlink", "should_escape": false,
			}},
		},
		"oauth_config": map[string]any{"scopes": map[string]any{"bot": append([]string(nil), botScopes...)}},
		"settings": map[string]any{
			"event_subscriptions":    map[string]any{"bot_events": append([]string(nil), botEvents...)},
			"interactivity":          map[string]any{"is_enabled": true},
			"org_deploy_enabled":     false,
			"socket_mode_enabled":    true,
			"token_rotation_enabled": false,
		},
	}
}
