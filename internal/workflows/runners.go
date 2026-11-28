package workflows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tunnel"
)

const maxShellOutputBytes = 256 * 1024

// AgentStepRequest describes an agent step execution.
type AgentStepRequest struct {
	RunID        string
	WorkflowName string
	StepName     string
	OwnerID      string
	AgentID      string
	SessionID    string // non-empty when reusing or resuming a session
	Prompt       string
	OnSession    func(sessionID string)
}

// AgentRunner executes workflow agent steps.
type AgentRunner interface {
	RunAgentStep(ctx context.Context, req AgentStepRequest) (sessionID string, output string, err error)
	CancelSession(ctx context.Context, sessionID string) error
}

// ShellStepRequest describes a shell command executed inside a target agent's sandbox.
type ShellStepRequest struct {
	RunID    string
	StepName string
	OwnerID  string
	AgentID  string
	Command  string
	Cwd      string
}

// ShellRunner executes workflow shell steps.
type ShellRunner interface {
	RunShellStep(ctx context.Context, req ShellStepRequest) (output string, exitCode int, err error)
}

// SessionAgentRunner runs workflow agent steps via session.Manager.
type SessionAgentRunner struct {
	Store    *store.Store
	Sessions *session.Manager
}

// RunAgentStep authorizes the owner on the target agent, creates or reuses a
// session, and runs one prompt turn to completion.
func (r *SessionAgentRunner) RunAgentStep(ctx context.Context, req AgentStepRequest) (string, string, error) {
	if r == nil || r.Sessions == nil || r.Store == nil {
		return "", "", errors.New("agent runner is not configured")
	}
	userName, err := authorizeOwnerAgent(ctx, r.Store, req.OwnerID, req.AgentID)
	if err != nil {
		return "", "", err
	}
	sessionID := req.SessionID
	if sessionID == "" {
		title := fmt.Sprintf("Workflow: %s / %s", req.WorkflowName, req.StepName)
		sess, err := r.Sessions.Create(ctx, req.AgentID, req.OwnerID, title)
		if err != nil {
			return "", "", err
		}
		sessionID = sess.ID
	}
	if req.OnSession != nil {
		req.OnSession(sessionID)
	}
	out, err := r.Sessions.Ask(ctx, sessionID, req.OwnerID, userName+" (workflow: "+req.WorkflowName+")", req.Prompt, session.PromptOpts{})
	return sessionID, out, err
}

// CancelSession cancels any active turn on sessionID.
func (r *SessionAgentRunner) CancelSession(ctx context.Context, sessionID string) error {
	if r == nil || r.Sessions == nil || sessionID == "" {
		return nil
	}
	err := r.Sessions.Cancel(ctx, sessionID)
	if errors.Is(err, session.ErrNoTurn) {
		return nil
	}
	return err
}

// TunnelShellRunner runs workflow shell commands inside an agent's sandbox via
// tunnel exec (StartHarnessInDir with sh -lc).
type TunnelShellRunner struct {
	Store *store.Store
	Wake  session.Waker
}

// RunShellStep wakes the target agent, executes req.Command via sh -lc, and
// returns captured output and exit code.
func (r *TunnelShellRunner) RunShellStep(ctx context.Context, req ShellStepRequest) (string, int, error) {
	if r == nil || r.Wake == nil || r.Store == nil {
		return "", -1, errors.New("shell runner is not configured")
	}
	if _, err := authorizeOwnerAgent(ctx, r.Store, req.OwnerID, req.AgentID); err != nil {
		return "", -1, err
	}
	ts, err := r.Wake(ctx, req.AgentID, nil)
	if err != nil {
		return "", -1, fmt.Errorf("wake agent sandbox: %w", err)
	}
	var (
		mu       sync.Mutex
		combined bytes.Buffer
	)
	writeLimited := func(p []byte) {
		mu.Lock()
		defer mu.Unlock()
		rem := maxShellOutputBytes - combined.Len()
		if rem <= 0 {
			return
		}
		if len(p) > rem {
			p = p[:rem]
		}
		_, _ = combined.Write(p)
	}
	proc, err := ts.StartHarnessInDir(ctx, []string{"sh", "-lc", req.Command}, req.Cwd, nil, func(line string) {
		writeLimited([]byte(line + "\n"))
	})
	if err != nil {
		return "", -1, fmt.Errorf("start shell command: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = proc.Close() })
	defer stop()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 16*1024)
		for {
			n, rerr := proc.Stdout.Read(buf)
			if n > 0 {
				writeLimited(buf[:n])
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					return
				}
				return
			}
		}
	}()

	<-proc.Done()
	<-readDone
	code, perr := proc.ExitCode()
	mu.Lock()
	output := strings.TrimRight(combined.String(), "\n")
	mu.Unlock()
	if ctx.Err() != nil {
		return output, code, ctx.Err()
	}
	if perr != nil {
		return output, code, perr
	}
	if code != 0 {
		return output, code, fmt.Errorf("command exited with status %d", code)
	}
	return output, 0, nil
}

func authorizeOwnerAgent(ctx context.Context, st *store.Store, ownerID, agentID string) (string, error) {
	agent, err := st.GetAgent(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("load agent %q: %w", agentID, err)
	}
	role, err := authz.Checker{Q: st.Queries}.RoleOf(ctx, ownerID, agent)
	if err != nil {
		return "", err
	}
	if !authz.Can(role, authz.AgentPrompt) {
		return "", errors.New("workflow owner no longer has access to target agent")
	}
	user, err := st.GetUser(ctx, ownerID)
	if err != nil {
		return "", err
	}
	if authz.UserRole(user) == config.RoleDisabled {
		return "", errors.New("workflow owner is disabled")
	}
	return user.Name, nil
}

// WakerFunc wakes an agent sandbox and returns its live tunnel session.
type WakerFunc = func(ctx context.Context, agentID string, status func(string)) (*tunnel.Session, error)
