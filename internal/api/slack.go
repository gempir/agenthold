package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
)

// Slack is the Slack integration (internal/slack) as the API sees it. Its
// errors may be *Error values with a status.
type Slack interface {
	Settings(ctx context.Context) (gen.SlackSettings, error)
	Configure(ctx context.Context, userID, botToken, appToken string) (gen.SlackSettings, error)
	Remove(ctx context.Context) error
	Manifest() map[string]any
	PeekLink(ctx context.Context, code string) (gen.SlackLinkPreview, error)
	Link(ctx context.Context, code, userID string) (gen.SlackIdentity, error)
	Links(ctx context.Context, userID string) ([]gen.SlackIdentity, error)
	Unlink(ctx context.Context, userID, teamID, slackUserID string) error
}

var errNoSlack = errStatus(http.StatusNotFound, "the Slack integration is not available on this server")

func (a *API) slackAdmin(ctx context.Context) (Slack, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Slack == nil {
		return nil, errNoSlack
	}
	return a.Slack, nil
}

// GetSlackSettings implements gen.StrictServerInterface.
func (a *API) GetSlackSettings(ctx context.Context, _ gen.GetSlackSettingsRequestObject) (gen.GetSlackSettingsResponseObject, error) {
	s, err := a.slackAdmin(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetSlackSettings200JSONResponse(out), nil
}

// ConfigureSlack implements gen.StrictServerInterface.
func (a *API) ConfigureSlack(ctx context.Context, req gen.ConfigureSlackRequestObject) (gen.ConfigureSlackResponseObject, error) {
	s, err := a.slackAdmin(ctx)
	if err != nil {
		return nil, err
	}
	bot, app := strings.TrimSpace(req.Body.BotToken), strings.TrimSpace(req.Body.AppToken)
	if !strings.HasPrefix(bot, "xoxb-") || len(bot) > 500 {
		return nil, badRequest("the bot token must be a Bot User OAuth Token (xoxb-…)")
	}
	if !strings.HasPrefix(app, "xapp-") || len(app) > 500 {
		return nil, badRequest("the app token must be an app-level token (xapp-…) with connections:write")
	}
	out, err := s.Configure(ctx, me(ctx).ID, bot, app)
	if err != nil {
		return nil, err
	}
	return gen.ConfigureSlack200JSONResponse(out), nil
}

// DeleteSlack implements gen.StrictServerInterface.
func (a *API) DeleteSlack(ctx context.Context, _ gen.DeleteSlackRequestObject) (gen.DeleteSlackResponseObject, error) {
	s, err := a.slackAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Remove(ctx); err != nil {
		return nil, err
	}
	return gen.DeleteSlack204Response{}, nil
}

// GetSlackManifest implements gen.StrictServerInterface.
func (a *API) GetSlackManifest(ctx context.Context, _ gen.GetSlackManifestRequestObject) (gen.GetSlackManifestResponseObject, error) {
	s, err := a.slackAdmin(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetSlackManifest200JSONResponse(s.Manifest()), nil
}

// ListSlackLinks implements gen.StrictServerInterface.
func (a *API) ListSlackLinks(ctx context.Context, _ gen.ListSlackLinksRequestObject) (gen.ListSlackLinksResponseObject, error) {
	if a.Slack == nil {
		return nil, errNoSlack
	}
	out, err := a.Slack.Links(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	return gen.ListSlackLinks200JSONResponse(out), nil
}

// GetSlackLink implements gen.StrictServerInterface.
func (a *API) GetSlackLink(ctx context.Context, req gen.GetSlackLinkRequestObject) (gen.GetSlackLinkResponseObject, error) {
	if a.Slack == nil {
		return nil, errNoSlack
	}
	out, err := a.Slack.PeekLink(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	return gen.GetSlackLink200JSONResponse(out), nil
}

// LinkSlack implements gen.StrictServerInterface.
func (a *API) LinkSlack(ctx context.Context, req gen.LinkSlackRequestObject) (gen.LinkSlackResponseObject, error) {
	if a.Slack == nil {
		return nil, errNoSlack
	}
	out, err := a.Slack.Link(ctx, strings.TrimSpace(req.Body.Code), me(ctx).ID)
	if err != nil {
		return nil, err
	}
	return gen.LinkSlack200JSONResponse(out), nil
}

// UnlinkSlack implements gen.StrictServerInterface.
func (a *API) UnlinkSlack(ctx context.Context, req gen.UnlinkSlackRequestObject) (gen.UnlinkSlackResponseObject, error) {
	if a.Slack == nil {
		return nil, errNoSlack
	}
	if err := a.Slack.Unlink(ctx, me(ctx).ID, req.TeamId, req.SlackUserId); err != nil {
		return nil, err
	}
	return gen.UnlinkSlack204Response{}, nil
}

// ErrorStatus returns the HTTP status and message an error from a handler
// would be answered with (for in-process callers such as the Slack app).
func ErrorStatus(err error) (int, string) { return toHTTP(err) }

// NewError returns an error the handlers' error handler answers with status.
func NewError(status int, msg string) error { return errStatus(status, msg) }

// IsAdmin reports whether the user in ctx manages the installation.
func (a *API) IsAdmin(ctx context.Context) bool { return a.requireAdmin(ctx) == nil }
