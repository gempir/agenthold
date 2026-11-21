package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
)

// roleRecheck is how often a long-lived connection (terminal, log and
// session streams, proxied preview WebSockets) re-reads its user: a role
// checked only when the connection opened would outlive a downgrade.
const roleRecheck = 2 * time.Second

// stillAllowed reports whether userID may still use this agenthold: the user
// exists and is not Disabled. A database error counts as allowed (the next
// check decides), so a short outage does not cut every connection.
func (a *API) stillAllowed(ctx context.Context, userID string) bool {
	u, err := a.Store.GetUser(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return err != nil || auth.Role(u) != config.RoleDisabled
}

// guardUser calls onEnd once, from its own goroutine, when userID loses
// access (checked every roleRecheck) while ctx lives. stop ends the watch.
func (a *API) guardUser(ctx context.Context, userID string, onEnd func()) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(roleRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !a.stillAllowed(ctx, userID) {
					if ctx.Err() == nil {
						onEnd()
					}
					return
				}
			}
		}
	}()
	return cancel
}

// guardSocket closes ws with a policy violation when userID loses access.
func (a *API) guardSocket(ctx context.Context, userID string, ws *websocket.Conn) (stop func()) {
	return a.guardUser(ctx, userID, func() { _ = ws.Close(websocket.StatusPolicyViolation, accessEnded) })
}

const accessEnded = "access ended: the account is disabled"

// isUpgrade reports whether r asks to switch protocols (a WebSocket).
func isUpgrade(r *http.Request) bool {
	for _, v := range r.Header.Values("Connection") {
		for f := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "upgrade") {
				return true
			}
		}
	}
	return false
}
