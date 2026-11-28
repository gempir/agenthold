package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/id"
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

// TaskPrompt is the first message of a thread that works on a task.
func TaskPrompt(p db.Project, t db.ProjectTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s of project %s: %s\n", t.ID, p.Name, t.Title)
	if body := strings.TrimSpace(t.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	b.WriteString("\nWork in this thread's workspace (see AGENTS.md) and commit your changes on its branch. " +
		"When you are done, call update_task with this task's id and status \"review\" and a short note on what you did; " +
		"if you cannot finish it, set it back to \"open\" with a note why.")
	return b.String()
}

// AssignOpts are the settings of AssignTask.
type AssignOpts struct {
	AgentID, UserID, UserName string
	Prompt                    session.PromptOpts
}

// AssignTask starts a thread on an agent that works on the task, and sends
// it the task.
func (s *Service) AssignTask(ctx context.Context, t db.ProjectTask, o AssignOpts) (db.ProjectTask, db.AgentSession, error) {
	if t.Status == TaskDone || t.Status == TaskCancelled {
		return t, db.AgentSession{}, fmt.Errorf("%w: the task is %s", ErrInvalid, t.Status)
	}
	p, err := s.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		return t, db.AgentSession{}, err
	}
	sess, err := s.StartThread(ctx, p, StartOpts{AgentID: o.AgentID, UserID: o.UserID, Title: t.Title})
	if err != nil {
		return t, sess, err
	}
	t.AgentID, t.SessionID, t.Status = o.AgentID, sess.ID, TaskInProgress
	if err := s.SaveTask(ctx, t); err != nil {
		return t, sess, err
	}
	if err := s.Sessions.PromptWith(ctx, sess.ID, o.UserID, o.UserName, TaskPrompt(p, t), nil, o.Prompt); err != nil {
		return t, sess, err
	}
	return t, sess, nil
}

// TaskInput is a new task.
type TaskInput struct {
	Title, Body, CreatedBy string
}

// CreateTask adds a task to a project.
func (s *Service) CreateTask(ctx context.Context, projectID string, in TaskInput) (db.ProjectTask, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > 200 {
		return db.ProjectTask{}, fmt.Errorf("%w: a task needs a title of up to 200 characters", ErrInvalid)
	}
	if len(in.Body) > 20000 {
		return db.ProjectTask{}, fmt.Errorf("%w: the task description is too long", ErrInvalid)
	}
	now := time.Now().Unix()
	t := db.ProjectTask{
		ID: id.New("tsk"), ProjectID: projectID, Title: title, Body: strings.TrimSpace(in.Body), Status: TaskOpen,
		CreatedBy: in.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	err := s.Store.CreateProjectTask(ctx, db.CreateProjectTaskParams(t))
	return t, err
}

// SaveTask stores a changed task.
func (s *Service) SaveTask(ctx context.Context, t db.ProjectTask) error {
	if !ValidTaskStatus(t.Status) {
		return fmt.Errorf("%w: unknown task status %q", ErrInvalid, t.Status)
	}
	if len(t.Note) > 4000 {
		t.Note = t.Note[:4000]
	}
	return s.Store.UpdateProjectTask(ctx, db.UpdateProjectTaskParams{
		Title: t.Title, Body: t.Body, Status: t.Status, AgentID: t.AgentID, SessionID: t.SessionID, Note: t.Note,
		UpdatedAt: time.Now().Unix(), ID: t.ID,
	})
}

// FinishThreadTasks marks the tasks a thread works on with status (after
// its branch was merged: done).
func (s *Service) FinishThreadTasks(ctx context.Context, sessionID, status string) {
	tasks, err := s.Store.ListSessionTasks(ctx, sessionID)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == TaskDone || t.Status == TaskCancelled {
			continue
		}
		t.Status = status
		if status == TaskOpen {
			t.AgentID, t.SessionID = "", ""
		}
		if err := s.SaveTask(ctx, t); err != nil {
			s.logger().Warn("update a task", "task", t.ID, "err", err)
		}
	}
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
