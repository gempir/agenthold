package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// StartOpts are the settings of a new project thread.
type StartOpts struct {
	AgentID, UserID, Title string
	// Direct works in the main checkouts instead of a workspace of its own.
	Direct bool
}

// StartThread creates a thread of the project on an agent. When the agent's
// machine runs, its workspace is prepared now (so mistakes show at once);
// otherwise before its first turn.
func (s *Service) StartThread(ctx context.Context, p db.Project, o StartOpts) (db.AgentSession, error) {
	if s.Sessions == nil {
		return db.AgentSession{}, errors.New("project threads are not available")
	}
	sess, err := s.Sessions.CreateWith(ctx, o.AgentID, o.UserID, o.Title, session.CreateOpts{})
	if err != nil {
		return sess, err
	}
	sess.ProjectID = p.ID
	if !o.Direct {
		sess.ProjectBranch = ThreadBranch(sess.ID)
	}
	if err := s.Store.SetSessionProject(ctx, db.SetSessionProjectParams{ProjectID: sess.ProjectID, ProjectBranch: sess.ProjectBranch, ID: sess.ID}); err != nil {
		return sess, err
	}
	sess.Cwd = ThreadCwd(p, sess)
	if ts, err := s.Machine(ctx, o.AgentID); err == nil && ts != nil {
		cwd, err := s.Prepare(ctx, ts, sess)
		if err != nil {
			_ = s.Sessions.Delete(ctx, sess.ID)
			return db.AgentSession{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
		}
		sess.Cwd = cwd
	}
	if err := s.Store.SetSessionGit(ctx, db.SetSessionGitParams{Cwd: sess.Cwd, ID: sess.ID}); err != nil {
		return sess, err
	}
	return sess, nil
}

// MachineFunc returns the tunnel to the running machine of an agent, or an
// error (tunnel.ErrNotConnected) when it does not run.
type MachineFunc func(ctx context.Context, agentID string) (*tunnel.Session, error)

// NodeMachine is a MachineFunc that reaches agents through n.
func NodeMachine(n node.Node) MachineFunc {
	return func(ctx context.Context, agentID string) (*tunnel.Session, error) {
		ok, err := n.Connected(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, tunnel.ErrNotConnected
		}
		return node.Client(n, agentID), nil
	}
}
