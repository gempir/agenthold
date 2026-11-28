package session

import "context"

// Notice records a notice in a session's log: shown to the people viewing the
// thread, never sent to the agent. Background jobs (the pull request
// watcher) use it to say what they did to a thread.
func (m *Manager) Notice(ctx context.Context, sessionID, message string) error {
	return m.log.append(ctx, sessionID, EventNotice, "", map[string]string{"message": message})
}
