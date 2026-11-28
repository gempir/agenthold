package session

import (
	"context"
	"time"

	"github.com/gempir/agenthold/internal/id"
)

// SecretRequest describes an in-thread request from an agent for a user secret.
// Values submitted by the user are never stored in the session event log.
type SecretRequest struct {
	RequestID   string   `json:"requestId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Hosts       []string `json:"hosts"`
	EnvVar      string   `json:"envVar,omitempty"`
	Header      string   `json:"header,omitempty"`
	Template    string   `json:"template,omitempty"`
}

// SecretResolution is delivered when the user submits or declines a SecretRequest.
// It never contains the plaintext secret value.
type SecretResolution struct {
	Status      string `json:"status"` // "submitted" | "declined"
	SecretID    string `json:"secretId,omitempty"`
	Name        string `json:"name,omitempty"`
	EnvVar      string `json:"envVar,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type pendingSecretReq struct {
	sessionID string
	agentID   string
	req       SecretRequest
	ch        chan SecretResolution
	createdAt time.Time
}

const maxPendingSecretAge = 30 * time.Minute

// RequestSecret emits a secret_request event into sessionID, wakes a snoozed
// thread, and optionally waits up to wait for the user to submit or decline it.
// If wait elapses before resolution, the request remains pending and res is nil.
func (m *Manager) RequestSecret(ctx context.Context, sessionID, agentID string, req SecretRequest, wait time.Duration) (string, *SecretResolution, error) {
	if sessionID == "" {
		return "", nil, ErrNotFound
	}
	reqID := id.New("sreq")
	req.RequestID = reqID
	ch := make(chan SecretResolution, 1)
	now := time.Now()

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return "", nil, errShuttingDown
	}
	if m.secretReqs == nil {
		m.secretReqs = map[string]*pendingSecretReq{}
	}
	// Prune stale unresolved requests so the map stays bounded.
	for k, p := range m.secretReqs {
		if now.Sub(p.createdAt) > maxPendingSecretAge {
			delete(m.secretReqs, k)
			close(p.ch)
		}
	}
	m.secretReqs[reqID] = &pendingSecretReq{
		sessionID: sessionID,
		agentID:   agentID,
		req:       req,
		ch:        ch,
		createdAt: now,
	}
	m.mu.Unlock()

	lctx := context.WithoutCancel(ctx)
	_, _ = m.store.UnsnoozeSession(lctx, sessionID)

	evData := map[string]any{
		"requestId":   reqID,
		"name":        req.Name,
		"description": req.Description,
		"hosts":       req.Hosts,
	}
	if req.EnvVar != "" {
		evData["envVar"] = req.EnvVar
	}
	if req.Header != "" {
		evData["header"] = req.Header
	}
	if err := m.log.append(lctx, sessionID, EventSecretRequest, "", evData); err != nil {
		m.mu.Lock()
		delete(m.secretReqs, reqID)
		m.mu.Unlock()
		return "", nil, err
	}

	if wait <= 0 {
		return reqID, nil, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res, ok := <-ch:
		if !ok {
			return reqID, nil, nil
		}
		return reqID, &res, nil
	case <-timer.C:
		return reqID, nil, nil
	case <-ctx.Done():
		return reqID, nil, ctx.Err()
	}
}

// GetSecretRequest returns the pending SecretRequest metadata for sessionID/requestID.
func (m *Manager) GetSecretRequest(sessionID, requestID string) (SecretRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.secretReqs[requestID]
	if p == nil || p.sessionID != sessionID {
		return SecretRequest{}, false
	}
	return p.req, true
}

// ResolveSecretRequest completes a pending secret request, waking any waiting
// agent tool call and recording a non-secret resolution event in the stream.
func (m *Manager) ResolveSecretRequest(ctx context.Context, sessionID, requestID, userID string, res SecretResolution) error {
	m.mu.Lock()
	p := m.secretReqs[requestID]
	if p == nil || p.sessionID != sessionID {
		m.mu.Unlock()
		return ErrUnknownSecretRequest
	}
	delete(m.secretReqs, requestID)
	p.ch <- res
	close(p.ch)
	m.mu.Unlock()

	evData := map[string]any{
		"requestId": requestID,
		"status":    res.Status,
		"by":        userID,
	}
	if res.SecretID != "" {
		evData["secretId"] = res.SecretID
	}
	if res.Name != "" {
		evData["name"] = res.Name
	}
	if res.EnvVar != "" {
		evData["envVar"] = res.EnvVar
	}
	if res.Placeholder != "" {
		evData["placeholder"] = res.Placeholder
	}
	return m.log.append(context.WithoutCancel(ctx), sessionID, EventSecretRequestResolved, userID, evData)
}

// RecordAccountConnectRequest emits an account_connect_request event into
// sessionID so the UI renders an inline one-click Connect card.
func (m *Manager) RecordAccountConnectRequest(ctx context.Context, sessionID string, data map[string]any) error {
	if sessionID == "" {
		return nil
	}
	lctx := context.WithoutCancel(ctx)
	_, _ = m.store.UnsnoozeSession(lctx, sessionID)
	return m.log.append(lctx, sessionID, EventAccountConnectRequest, "", data)
}
