package api

import (
	"context"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/push"
	"github.com/gempir/agenthold/internal/store/db"
)

// GetPushVapidKey implements gen.StrictServerInterface.
func (a *API) GetPushVapidKey(_ context.Context, _ gen.GetPushVapidKeyRequestObject) (gen.GetPushVapidKeyResponseObject, error) {
	out := gen.GetPushVapidKey200JSONResponse{}
	if a.Push != nil {
		out.Enabled = true
		out.PublicKey = a.Push.PublicKey()
	}
	return out, nil
}

// ListPushSubscriptions implements gen.StrictServerInterface.
func (a *API) ListPushSubscriptions(ctx context.Context, _ gen.ListPushSubscriptionsRequestObject) (gen.ListPushSubscriptionsResponseObject, error) {
	rows, err := a.Store.ListPushSubscriptions(ctx, me(ctx).ID)
	out := gen.ListPushSubscriptions200JSONResponse{}
	for _, r := range rows {
		out = append(out, gen.PushSubscription{Id: r.ID, Endpoint: r.Endpoint, UserAgent: r.UserAgent, CreatedAt: r.CreatedAt})
	}
	return out, err
}

// RegisterPushSubscription implements gen.StrictServerInterface.
func (a *API) RegisterPushSubscription(ctx context.Context, req gen.RegisterPushSubscriptionRequestObject) (gen.RegisterPushSubscriptionResponseObject, error) {
	if a.Push == nil {
		return nil, errStatus(503, "push unavailable")
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("missing subscription")
	}
	if err := push.Validate(b.Endpoint, b.P256dh, b.Auth); err != nil {
		return nil, badRequest(err.Error())
	}
	ua := ""
	if b.UserAgent != nil {
		ua = *b.UserAgent
	}
	if len(ua) > 512 || len(b.Endpoint) > 4096 {
		return nil, badRequest("subscription too large")
	}
	err := a.Store.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: id.New("push"), UserID: me(ctx).ID, Endpoint: b.Endpoint, P256dh: b.P256dh, Auth: b.Auth, UserAgent: ua, CreatedAt: a.Now().Unix()})
	if err != nil {
		return nil, err
	}
	rows, err := a.ListPushSubscriptions(ctx, gen.ListPushSubscriptionsRequestObject{})
	if err != nil {
		return nil, err
	}
	return gen.RegisterPushSubscription200JSONResponse(rows.(gen.ListPushSubscriptions200JSONResponse)), nil
}

// DeletePushSubscription implements gen.StrictServerInterface.
func (a *API) DeletePushSubscription(ctx context.Context, req gen.DeletePushSubscriptionRequestObject) (gen.DeletePushSubscriptionResponseObject, error) {
	err := a.Store.DeletePushSubscription(ctx, db.DeletePushSubscriptionParams{ID: req.SubscriptionId, UserID: me(ctx).ID})
	return gen.DeletePushSubscription200JSONResponse{Ok: true}, err
}

// TestPush implements gen.StrictServerInterface.
func (a *API) TestPush(ctx context.Context, _ gen.TestPushRequestObject) (gen.TestPushResponseObject, error) {
	if a.Push == nil {
		return nil, errStatus(503, "push unavailable")
	}
	a.Push.NotifyUser(ctx, me(ctx).ID, push.Notification{Title: "agenthold", Body: "Notifications are enabled", URL: "/", Tag: "test"})
	return gen.TestPush200JSONResponse{Ok: true}, nil
}
