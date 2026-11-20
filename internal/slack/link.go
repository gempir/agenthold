package slack

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/store/db"
)

// linkTTL is how long a link code works.
const linkTTL = 15 * time.Minute

var errUnknownCode = api.NewError(http.StatusNotFound, "this link is unknown, used or expired; ask the bot for a new one with /agenthold link")

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// linkedUser returns the agenthold user linked to a Slack user, and a
// context acting as them.
func (s *Service) linkedUser(ctx context.Context, team, slackUser string) (context.Context, *db.User, bool, error) {
	row, err := s.o.Store.GetSlackUser(ctx, db.GetSlackUserParams{TeamID: team, SlackUserID: slackUser})
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, nil, false, nil
	}
	if err != nil {
		return ctx, nil, false, err
	}
	uctx, u, err := s.o.Auth.UserContext(ctx, row.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, nil, false, nil
	}
	if err != nil {
		return ctx, nil, false, err
	}
	return auth.WithVia(uctx, "slack"), u, true, nil
}

// userContext acts as an agenthold user.
func (s *Service) userContext(ctx context.Context, userID string) (context.Context, error) {
	uctx, _, err := s.o.Auth.UserContext(ctx, userID)
	if err != nil {
		return ctx, err
	}
	return auth.WithVia(uctx, "slack"), nil
}

// newLinkCode issues a code that links the Slack user.
func (s *Service) newLinkCode(ctx context.Context, team, slackUser string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(b)
	name := slackUser
	if c := s.api(); c != nil {
		if u, err := c.GetUserInfoContext(ctx, slackUser); err == nil {
			switch {
			case u.Profile.DisplayName != "":
				name = u.Profile.DisplayName
			case u.RealName != "":
				name = u.RealName
			case u.Name != "":
				name = u.Name
			}
		}
	}
	now := s.now()
	_ = s.o.Store.DeleteExpiredSlackLinkCodes(ctx, now.Unix())
	_ = s.o.Store.DeleteSlackLinkCodesOf(ctx, db.DeleteSlackLinkCodesOfParams{TeamID: team, SlackUserID: slackUser})
	err := s.o.Store.CreateSlackLinkCode(ctx, db.CreateSlackLinkCodeParams{
		CodeHash: hashCode(code), TeamID: team, SlackUserID: slackUser, SlackName: name,
		TeamName: s.installation().teamName, ExpiresAt: now.Add(linkTTL).Unix(),
	})
	return code, err
}

func (s *Service) linkURL(code string) string {
	return s.o.PublicURL + "/slack/link?code=" + url.QueryEscape(code)
}

// linkBlocks invite a Slack user to link their account.
func (s *Service) linkBlocks(ctx context.Context, team, slackUser string) ([]slackgo.Block, error) {
	code, err := s.newLinkCode(ctx, team, slackUser)
	if err != nil {
		return nil, err
	}
	b := linkButton("link", "Connect your agenthold account", s.linkURL(code))
	b.Style = slackgo.StylePrimary
	return []slackgo.Block{
		sectionBlock("To chat with agents here, connect your Slack account to agenthold. Everything you do in Slack then runs as you, with your access."),
		slackgo.NewActionBlock("link", b),
		contextBlock("The link works once, for 15 minutes."),
	}, nil
}

// askToLink shows the link prompt to an unlinked Slack user.
func (s *Service) askToLink(ctx context.Context, r reply, team string) {
	blocks, err := s.linkBlocks(ctx, team, r.user)
	if err != nil {
		s.lg.Warn("issue link code", "err", err)
		return
	}
	s.send(ctx, r, "Connect your agenthold account", blocks)
}

func (s *Service) lookupCode(ctx context.Context, code string) (db.SlackLinkCode, string, error) {
	if code == "" || len(code) > 200 {
		return db.SlackLinkCode{}, "", errUnknownCode
	}
	h := hashCode(code)
	row, err := s.o.Store.GetSlackLinkCode(ctx, h)
	if errors.Is(err, sql.ErrNoRows) {
		return row, h, errUnknownCode
	}
	if err != nil {
		return row, h, err
	}
	if subtle.ConstantTimeCompare([]byte(row.CodeHash), []byte(h)) != 1 {
		return row, h, errUnknownCode
	}
	if row.ExpiresAt < s.now().Unix() {
		_, _ = s.o.Store.DeleteSlackLinkCode(ctx, h)
		return row, h, errUnknownCode
	}
	return row, h, nil
}

// PeekLink implements api.Slack.
func (s *Service) PeekLink(ctx context.Context, code string) (gen.SlackLinkPreview, error) {
	row, _, err := s.lookupCode(ctx, code)
	if err != nil {
		return gen.SlackLinkPreview{}, err
	}
	return gen.SlackLinkPreview{
		TeamId: row.TeamID, TeamName: row.TeamName, SlackUserId: row.SlackUserID, SlackUserName: row.SlackName,
		ExpiresAt: time.Unix(row.ExpiresAt, 0).UTC(),
	}, nil
}

// Link implements api.Slack: the code is used up, and the Slack user acts
// as userID from now on.
func (s *Service) Link(ctx context.Context, code, userID string) (gen.SlackIdentity, error) {
	row, h, err := s.lookupCode(ctx, code)
	if err != nil {
		return gen.SlackIdentity{}, err
	}
	if n, err := s.o.Store.DeleteSlackLinkCode(ctx, h); err != nil {
		return gen.SlackIdentity{}, err
	} else if n != 1 {
		return gen.SlackIdentity{}, errUnknownCode // used concurrently
	}
	now := s.now()
	if err := s.o.Store.PutSlackUser(ctx, db.PutSlackUserParams{
		TeamID: row.TeamID, SlackUserID: row.SlackUserID, UserID: userID, SlackName: row.SlackName,
		TeamName: row.TeamName, CreatedAt: now.Unix(),
	}); err != nil {
		return gen.SlackIdentity{}, err
	}
	name := "your account"
	if u, err := s.o.Store.GetUser(ctx, userID); err == nil && u.Name != "" {
		name = "*" + escape(u.Name) + "*"
	}
	slackUser := row.SlackUserID
	s.goTracked(func() {
		text := ":white_check_mark: Your Slack account is now connected to agenthold as " + name + ". Message me here, mention me in a channel, or type `/agenthold help`."
		_, _ = s.post(s.ctx, slackUser, "", "Connected to agenthold", []slackgo.Block{sectionBlock(text)})
	})
	return gen.SlackIdentity{TeamId: row.TeamID, TeamName: row.TeamName, SlackUserId: row.SlackUserID, SlackUserName: row.SlackName, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

// Links implements api.Slack.
func (s *Service) Links(ctx context.Context, userID string) ([]gen.SlackIdentity, error) {
	rows, err := s.o.Store.ListUserSlackUsers(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []gen.SlackIdentity{}
	for _, r := range rows {
		out = append(out, gen.SlackIdentity{TeamId: r.TeamID, TeamName: r.TeamName, SlackUserId: r.SlackUserID, SlackUserName: r.SlackName, CreatedAt: time.Unix(r.CreatedAt, 0).UTC()})
	}
	return out, nil
}

// Unlink implements api.Slack.
func (s *Service) Unlink(ctx context.Context, userID, teamID, slackUserID string) error {
	n, err := s.o.Store.DeleteSlackUser(ctx, db.DeleteSlackUserParams{TeamID: teamID, SlackUserID: slackUserID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return api.NewError(http.StatusNotFound, "not found")
	}
	return nil
}
