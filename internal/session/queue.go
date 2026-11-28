package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

// ErrQueueMessageNotFound is returned when a queued message does not exist.
var ErrQueueMessageNotFound = errors.New("queued message not found")

// QueuedMessage is a follow-up message waiting to run on a session.
type QueuedMessage struct {
	ID        string        `json:"id"`
	SessionID string        `json:"sessionId"`
	UserID    string        `json:"userId"`
	UserName  string        `json:"userName"`
	Text      string        `json:"text"`
	Images    []PromptImage `json:"images,omitempty"`
	Steer     bool          `json:"steer"`
	CreatedAt time.Time     `json:"createdAt"`
}

// QueueUpdate is broadcast over the session stream whenever the queue changes.
type QueueUpdate struct {
	SessionID string          `json:"sessionId"`
	Messages  []QueuedMessage `json:"messages"`
	Count     int             `json:"count"`
}

func queuedMessageFromRow(r db.SessionQueuedMessage) QueuedMessage {
	qm := QueuedMessage{
		ID:        r.ID,
		SessionID: r.SessionID,
		UserID:    r.UserID,
		UserName:  r.UserName,
		Text:      r.Text,
		Steer:     r.Steer != 0,
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
	if r.ImagesJson != "" {
		var imgs []PromptImage
		if json.Unmarshal([]byte(r.ImagesJson), &imgs) == nil && len(imgs) > 0 {
			qm.Images = imgs
		}
	}
	return qm
}

func encodeQueuedImages(images []PromptImage) string {
	if len(images) == 0 {
		return ""
	}
	b, err := json.Marshal(images)
	if err != nil {
		return ""
	}
	return string(b)
}

// ListQueue returns the session's queued messages in execution order.
func (m *Manager) ListQueue(ctx context.Context, sessionID string) ([]QueuedMessage, error) {
	rows, err := m.store.ListSessionQueuedMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]QueuedMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, queuedMessageFromRow(r))
	}
	return out, nil
}

// broadcastQueue pushes the latest queue snapshot to all live subscribers.
func (m *Manager) broadcastQueue(ctx context.Context, sessionID string) {
	msgs, err := m.ListQueue(context.WithoutCancel(ctx), sessionID)
	if err != nil {
		m.lg.Warn("list queue for broadcast", "session", sessionID, "err", err)
		return
	}
	m.log.broadcastQueue(sessionID, QueueUpdate{
		SessionID: sessionID,
		Messages:  msgs,
		Count:     len(msgs),
	})
}

// Enqueue adds a follow-up message to the session's queue. If the session is
// idle, it starts the turn immediately and returns started=true. If the session
// is running and steer is true, it promotes the message and interrupts the
// active turn so this message runs immediately next.
func (m *Manager) Enqueue(ctx context.Context, sessionID, userID, userName, text string, images []PromptImage, steer bool) (bool, *QueuedMessage, error) {
	if _, err := m.store.GetAgentSession(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		return false, nil, ErrNotFound
	} else if err != nil {
		return false, nil, err
	}

	m.qmu.Lock()
	if !m.Running(sessionID) {
		m.qmu.Unlock()
		err := m.PromptWith(ctx, sessionID, userID, userName, text, nil, PromptOpts{Images: images})
		if err == nil {
			return true, nil, nil
		}
		if !errors.Is(err, ErrBusy) {
			return false, nil, err
		}
		m.qmu.Lock()
	}

	now := time.Now().UTC()
	var steerVal int64
	if steer {
		steerVal = 1
	}
	row := db.EnqueueSessionMessageParams{
		ID:         id.New("qm"),
		SessionID:  sessionID,
		UserID:     userID,
		UserName:   userName,
		Text:       text,
		ImagesJson: encodeQueuedImages(images),
		Steer:      steerVal,
		CreatedAt:  now.UnixMilli(),
	}
	if err := m.store.EnqueueSessionMessage(ctx, row); err != nil {
		m.qmu.Unlock()
		return false, nil, err
	}
	m.qmu.Unlock()

	m.broadcastQueue(ctx, sessionID)

	qm := QueuedMessage{
		ID:        row.ID,
		SessionID: row.SessionID,
		UserID:    row.UserID,
		UserName:  row.UserName,
		Text:      row.Text,
		Images:    images,
		Steer:     steer,
		CreatedAt: now,
	}

	if steer {
		m.triggerSteer(ctx, sessionID)
	}
	return false, &qm, nil
}

// UpdateQueuedMessage edits the text of a queued message.
func (m *Manager) UpdateQueuedMessage(ctx context.Context, sessionID, messageID, text string) (QueuedMessage, error) {
	n, err := m.store.UpdateSessionQueuedMessage(ctx, db.UpdateSessionQueuedMessageParams{
		Text:      text,
		ID:        messageID,
		SessionID: sessionID,
	})
	if err != nil {
		return QueuedMessage{}, err
	}
	if n == 0 {
		return QueuedMessage{}, ErrQueueMessageNotFound
	}
	row, err := m.store.GetSessionQueuedMessage(ctx, db.GetSessionQueuedMessageParams{
		ID:        messageID,
		SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QueuedMessage{}, ErrQueueMessageNotFound
		}
		return QueuedMessage{}, err
	}
	m.broadcastQueue(ctx, sessionID)
	return queuedMessageFromRow(row), nil
}

// DeleteQueuedMessage removes a queued message.
func (m *Manager) DeleteQueuedMessage(ctx context.Context, sessionID, messageID string) error {
	n, err := m.store.DeleteSessionQueuedMessage(ctx, db.DeleteSessionQueuedMessageParams{
		ID:        messageID,
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrQueueMessageNotFound
	}
	m.broadcastQueue(ctx, sessionID)
	return nil
}

// SendQueuedMessageNow promotes messageID to run immediately: starts a turn now
// if the session is idle, or marks it as steer and interrupts the current turn
// so it drains next.
func (m *Manager) SendQueuedMessageNow(ctx context.Context, sessionID, messageID string) (started bool, steered bool, err error) {
	m.qmu.Lock()
	row, err := m.store.GetSessionQueuedMessage(ctx, db.GetSessionQueuedMessageParams{
		ID:        messageID,
		SessionID: sessionID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		m.qmu.Unlock()
		return false, false, ErrQueueMessageNotFound
	}
	if err != nil {
		m.qmu.Unlock()
		return false, false, err
	}

	if !m.Running(sessionID) {
		if _, err := m.store.DeleteSessionQueuedMessage(ctx, db.DeleteSessionQueuedMessageParams{
			ID:        messageID,
			SessionID: sessionID,
		}); err != nil {
			m.qmu.Unlock()
			return false, false, err
		}
		m.qmu.Unlock()
		qm := queuedMessageFromRow(row)
		err := m.PromptWith(ctx, sessionID, qm.UserID, qm.UserName, qm.Text, nil, PromptOpts{Images: qm.Images})
		if err == nil {
			m.broadcastQueue(ctx, sessionID)
			return true, false, nil
		}
		if !errors.Is(err, ErrBusy) {
			// Restore message on non-busy error.
			_ = m.store.EnqueueSessionMessage(context.WithoutCancel(ctx), db.EnqueueSessionMessageParams(row))
			return false, false, err
		}
		// Lost race: re-insert as steer and interrupt the turn that beat us.
		_ = m.store.EnqueueSessionMessage(context.WithoutCancel(ctx), db.EnqueueSessionMessageParams{
			ID: row.ID, SessionID: row.SessionID, UserID: row.UserID, UserName: row.UserName,
			Text: row.Text, ImagesJson: row.ImagesJson, Steer: 1, CreatedAt: row.CreatedAt,
		})
		m.broadcastQueue(ctx, sessionID)
		m.triggerSteer(ctx, sessionID)
		return false, true, nil
	}

	if _, err := m.store.SetSessionQueuedMessageSteer(ctx, db.SetSessionQueuedMessageSteerParams{
		Steer:     1,
		ID:        messageID,
		SessionID: sessionID,
	}); err != nil {
		m.qmu.Unlock()
		return false, false, err
	}
	m.qmu.Unlock()
	m.broadcastQueue(ctx, sessionID)
	m.triggerSteer(ctx, sessionID)
	return false, true, nil
}

// triggerSteer marks the running turn as steering and interrupts it so the
// turn's completion handler immediately drains the steer message. If no turn
// is running anymore, it drains directly.
func (m *Manager) triggerSteer(ctx context.Context, sessionID string) {
	m.mu.Lock()
	t := m.turns[sessionID]
	if t != nil && !t.finished {
		t.steering = true
	}
	m.mu.Unlock()
	if err := m.Cancel(ctx, sessionID); errors.Is(err, ErrNoTurn) {
		m.drainQueue(context.WithoutCancel(ctx), sessionID)
	}
}

// drainQueue pops the highest-priority queued message (steer first, then FIFO)
// and starts the next turn if the session is idle and the manager is open.
func (m *Manager) drainQueue(ctx context.Context, sessionID string) {
	if m.shuttingDown() {
		return
	}
	m.qmu.Lock()
	if m.Running(sessionID) {
		m.qmu.Unlock()
		return
	}
	rows, err := m.store.ListSessionQueuedMessages(ctx, sessionID)
	if err != nil {
		m.qmu.Unlock()
		m.lg.Warn("drain queue: list failed", "session", sessionID, "err", err)
		return
	}
	if len(rows) == 0 {
		m.qmu.Unlock()
		return
	}
	first := rows[0]
	n, err := m.store.DeleteSessionQueuedMessage(ctx, db.DeleteSessionQueuedMessageParams{
		ID:        first.ID,
		SessionID: sessionID,
	})
	m.qmu.Unlock()
	if err != nil || n == 0 {
		return
	}
	m.broadcastQueue(ctx, sessionID)

	qm := queuedMessageFromRow(first)
	if err := m.PromptWith(ctx, sessionID, qm.UserID, qm.UserName, qm.Text, nil, PromptOpts{Images: qm.Images}); err != nil {
		if errors.Is(err, ErrBusy) {
			// Another prompt beat us; restore so it runs after that turn.
			_ = m.store.EnqueueSessionMessage(ctx, db.EnqueueSessionMessageParams(first))
			m.broadcastQueue(ctx, sessionID)
			return
		}
		m.lg.Warn("drain queue: start turn failed", "session", sessionID, "message", first.ID, "err", err)
	}
}
