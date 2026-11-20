package slack

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
)

var errNotConfigured = errors.New("slack is not configured")

func plain(s string) *slackgo.TextBlockObject {
	return slackgo.NewTextBlockObject(slackgo.PlainTextType, s, false, false)
}

func mrkdwn(s string) *slackgo.TextBlockObject {
	return slackgo.NewTextBlockObject(slackgo.MarkdownType, s, false, false)
}

func sectionBlock(text string) *slackgo.SectionBlock {
	return slackgo.NewSectionBlock(mrkdwn(text), nil, nil)
}

func contextBlock(text string) *slackgo.ContextBlock {
	return slackgo.NewContextBlock("", mrkdwn(text))
}

// markdownBlock holds standard Markdown (the agent's text).
func markdownBlock(text string) *slackgo.MarkdownBlock {
	return slackgo.NewMarkdownBlock("", text)
}

func linkButton(actionID, text, url string) *slackgo.ButtonBlockElement {
	b := slackgo.NewButtonBlockElement(actionID, "", plain(text))
	b.URL = url
	return b
}

// escape makes text safe inside Slack mrkdwn.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// tail is the end of s: at most n bytes, starting on a rune boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// post posts a message (in a thread when threadTS is set) and returns its ts.
func (s *Service) post(ctx context.Context, channel, threadTS, text string, blocks []slackgo.Block) (string, error) {
	_, ts, err := s.postTo(ctx, channel, threadTS, text, blocks)
	return ts, err
}

// postTo posts and returns the channel too (posting to a user id opens
// the app's DM with them).
func (s *Service) postTo(ctx context.Context, channel, threadTS, text string, blocks []slackgo.Block) (string, string, error) {
	c := s.api()
	if c == nil {
		return "", "", errNotConfigured
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false), slackgo.MsgOptionBlocks(blocks...), slackgo.MsgOptionDisableLinkUnfurl()}
	if threadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(threadTS))
	}
	ch, ts, err := c.PostMessageContext(ctx, channel, opts...)
	if err != nil && ctx.Err() == nil {
		s.lg.Warn("post message", "channel", channel, "err", slackError(err))
	}
	return ch, ts, err
}

func (s *Service) update(ctx context.Context, channel, ts, text string, blocks []slackgo.Block) error {
	c := s.api()
	if c == nil {
		return errNotConfigured
	}
	_, _, _, err := c.UpdateMessageContext(ctx, channel, ts, slackgo.MsgOptionText(text, false), slackgo.MsgOptionBlocks(blocks...))
	if err != nil && ctx.Err() == nil {
		s.lg.Warn("update message", "channel", channel, "err", slackError(err))
	}
	return err
}

const waitingReaction = "hourglass_flowing_sand"

func (s *Service) react(ctx context.Context, channel, ts string) error {
	c := s.api()
	if c == nil {
		return errNotConfigured
	}
	return c.AddReactionContext(ctx, waitingReaction, slackgo.NewRefToMessage(channel, ts))
}

func (s *Service) unreact(ctx context.Context, channel, ts string) error {
	c := s.api()
	if c == nil {
		return errNotConfigured
	}
	return c.RemoveReactionContext(ctx, waitingReaction, slackgo.NewRefToMessage(channel, ts))
}

// reply is where an answer to one person goes: the response URL of a slash
// command or an interaction, or an ephemeral message in a channel.
type reply struct {
	responseURL string
	channel     string
	threadTS    string
	user        string // Slack user
}

// send shows blocks to the person only.
func (s *Service) send(ctx context.Context, r reply, text string, blocks []slackgo.Block) {
	if r.responseURL != "" {
		bs := slackgo.Blocks{BlockSet: blocks}
		msg := &slackgo.WebhookMessage{Text: text, Blocks: &bs, ResponseType: slackgo.ResponseTypeEphemeral}
		if err := slackgo.PostWebhookCustomHTTPContext(ctx, r.responseURL, s.o.HTTPClient, msg); err != nil && ctx.Err() == nil {
			s.lg.Warn("respond", "err", err)
		}
		return
	}
	c := s.api()
	if c == nil || r.channel == "" || r.user == "" {
		return
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false), slackgo.MsgOptionBlocks(blocks...)}
	if r.threadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(r.threadTS))
	}
	if _, err := c.PostEphemeralContext(ctx, r.channel, r.user, opts...); err != nil && ctx.Err() == nil {
		s.lg.Warn("post ephemeral", "channel", r.channel, "err", slackError(err))
	}
}

// say shows a line of text to the person only.
func (s *Service) say(ctx context.Context, r reply, text string) {
	s.send(ctx, r, text, []slackgo.Block{sectionBlock(text)})
}

// deleteOriginal removes the ephemeral message an interaction came from.
func (s *Service) deleteOriginal(ctx context.Context, responseURL string) {
	if responseURL == "" {
		return
	}
	_ = slackgo.PostWebhookCustomHTTPContext(ctx, responseURL, s.o.HTTPClient, &slackgo.WebhookMessage{DeleteOriginal: true})
}

// errorText is what a person is told about a failed action.
func (s *Service) errorText(err error) string {
	status, msg := api.ErrorStatus(err)
	switch status {
	case http.StatusInternalServerError:
		s.lg.Warn("slack action failed", "err", err)
		return "something went wrong on the server"
	case http.StatusForbidden:
		if msg == "forbidden" {
			return "you don't have access to that"
		}
	case http.StatusNotFound:
		if msg == "not found" {
			return "it does not exist, or you don't have access to it"
		}
	}
	return msg
}

// tellError shows a failed action to the Slack user who tried it.
func (s *Service) tellError(ctx context.Context, channel, threadTS, slackUser, what string, err error) {
	s.say(ctx, reply{channel: channel, threadTS: threadTS, user: slackUser}, ":warning: "+what+": "+escape(s.errorText(err))+".")
}

func stateEmoji(st gen.SandboxState) string {
	switch st {
	case gen.SandboxStateRunning:
		return ":large_green_circle:"
	case gen.SandboxStateSleeping:
		return ":zzz:"
	case gen.SandboxStateStarting, gen.SandboxStateCreating, gen.SandboxStateRebuilding, gen.SandboxStateStopping:
		return ":hourglass_flowing_sand:"
	case gen.SandboxStateUnreachable:
		return ":warning:"
	case gen.SandboxStateError:
		return ":red_circle:"
	}
	return ":white_circle:"
}

func (s *Service) chatURL(agentID, sessionID string) string {
	u := s.o.PublicURL + "/a/" + agentID
	if sessionID != "" {
		u += "?s=" + sessionID
	}
	return u
}
