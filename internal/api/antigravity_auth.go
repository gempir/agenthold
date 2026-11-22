package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/harness/antigravity"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/tunnel"
)

const (
	authFlowURLTimeout    = 25 * time.Second
	authFlowSubmitTimeout = 30 * time.Second
	authFlowTTL           = 10 * time.Minute
)

type activeHarnessAuth struct {
	id      string
	agentID string
	userID  string
	authURL antigravity.AuthorizationURL
	proc    *tunnel.Process
	cancel  context.CancelFunc
	timer   *time.Timer

	mu         sync.Mutex
	lastOutput string
}

func (f *activeHarnessAuth) noteOutput(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	f.mu.Lock()
	f.lastOutput = line
	f.mu.Unlock()
}

func (f *activeHarnessAuth) output() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOutput
}

func (f *activeHarnessAuth) close() {
	if f.timer != nil {
		f.timer.Stop()
	}
	if f.proc != nil {
		_ = f.proc.Close()
	}
	if f.cancel != nil {
		f.cancel()
	}
}

type harnessAuthManager struct {
	mu    sync.Mutex
	flows map[string]*activeHarnessAuth // keyed by agentID (at most one active flow per agent)
}

func newHarnessAuthManager() *harnessAuthManager {
	return &harnessAuthManager{flows: map[string]*activeHarnessAuth{}}
}

func (m *harnessAuthManager) put(flow *activeHarnessAuth) {
	m.mu.Lock()
	prev := m.flows[flow.agentID]
	m.flows[flow.agentID] = flow
	m.mu.Unlock()
	if prev != nil {
		prev.close()
	}
}

func (m *harnessAuthManager) take(agentID, flowID string) *activeHarnessAuth {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.flows[agentID]
	if f == nil || (flowID != "" && f.id != flowID) {
		return nil
	}
	delete(m.flows, agentID)
	if f.timer != nil {
		f.timer.Stop()
	}
	return f
}

func (m *harnessAuthManager) cancel(agentID, flowID string) {
	if f := m.take(agentID, flowID); f != nil {
		f.close()
	}
}

// StartHarnessAuth implements gen.StrictServerInterface.
// Launches `agy acp --disable-telemetry --login` inside the agent's sandbox,
// captures the Google OAuth sign-in URL, and keeps the process alive waiting
// for SubmitHarnessAuth to feed back the authorization code or callback URL.
func (a *API) StartHarnessAuth(ctx context.Context, req gen.StartHarnessAuthRequestObject) (gen.StartHarnessAuthResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentAuthLogin)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	if ag.Harness != "antigravity" {
		return nil, badRequest("interactive browser sign-in is only supported for the Antigravity harness")
	}
	ad, ok := harness.Get(ag.Harness)
	if !ok {
		return nil, errStatus(http.StatusInternalServerError, "antigravity harness adapter is not registered")
	}

	ts, err := a.Wake(ctx, ag.ID, func(string) {})
	if err != nil {
		return nil, err
	}

	cmd := a.AntigravityLoginCmd
	if len(cmd) == 0 {
		cmd = []string{"agy", "acp", "--disable-telemetry", "--login"}
	}
	env := ad.Environment(harness.Config{Model: ag.Model})

	// Use a detached context for the long-lived sign-in process so it survives
	// after this HTTP handler returns (especially across remote edge nodes,
	// where stream lifetimes follow the opener's context).
	procCtx, procCancel := context.WithTimeout(context.Background(), authFlowTTL+time.Minute)
	flow := &activeHarnessAuth{
		id:      id.New("af"),
		agentID: ag.ID,
		userID:  u.ID,
		cancel:  procCancel,
	}
	urlCh := make(chan antigravity.AuthorizationURL, 1)
	inspectLine := func(line string) {
		flow.noteOutput(line)
		if parsed, ok := antigravity.ExtractAuthorizationURL(line); ok {
			select {
			case urlCh <- parsed:
			default:
			}
		}
	}

	proc, err := ts.StartHarness(procCtx, cmd, env, inspectLine)
	if err != nil {
		procCancel()
		return nil, errStatus(http.StatusConflict, fmt.Sprintf("could not start Antigravity sign-in: %v", err))
	}
	flow.proc = proc

	// Drain stdout continuously so the process never blocks on pipe writes,
	// scanning every line for the Google OAuth URL.
	go func() {
		sc := bufio.NewScanner(proc.Stdout)
		sc.Buffer(make([]byte, 4096), 256*1024)
		for sc.Scan() {
			inspectLine(sc.Text())
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, authFlowURLTimeout)
	defer cancel()

	select {
	case parsed := <-urlCh:
		flow.authURL = parsed
		flow.timer = time.AfterFunc(authFlowTTL, func() {
			a.authFlows.cancel(flow.agentID, flow.id)
		})
		a.authFlows.put(flow)
		uStr := parsed.URL
		return gen.StartHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
			FlowId:           flow.id,
			Status:           gen.HarnessAuthFlowStatusWaitingForCode,
			AuthorizationUrl: &uStr,
		}), nil

	case <-proc.Done():
		code, procErr := proc.ExitCode()
		flow.close()
		if procErr == nil && code == 0 {
			_ = a.Store.MarkCredentialsInSandbox(context.WithoutCancel(ctx), ag.ID)
			a.Sessions.Disconnect(ag.ID)
			a.record(ctx, ag, audit.HarnessLogin, "oauth-personal", nil)
			msg := "Antigravity is already signed in."
			return gen.StartHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
				FlowId:  flow.id,
				Status:  gen.HarnessAuthFlowStatusCompleted,
				Message: &msg,
			}), nil
		}
		detail := flow.output()
		if detail == "" && procErr != nil {
			detail = procErr.Error()
		}
		if detail == "" {
			detail = fmt.Sprintf("sign-in exited with code %d before producing a login URL", code)
		}
		return nil, errStatus(http.StatusConflict, detail)

	case <-waitCtx.Done():
		flow.close()
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		detail := flow.output()
		if detail != "" {
			return nil, errStatus(http.StatusGatewayTimeout, "timed out waiting for Google sign-in URL: "+detail)
		}
		return nil, errStatus(http.StatusGatewayTimeout, "timed out waiting for Antigravity to print a Google sign-in URL")
	}
}

// SubmitHarnessAuth implements gen.StrictServerInterface.
func (a *API) SubmitHarnessAuth(ctx context.Context, req gen.SubmitHarnessAuthRequestObject) (gen.SubmitHarnessAuthResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentAuthLogin)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.FlowId) == "" {
		return nil, badRequest("flowId is required")
	}
	flow := a.authFlows.take(req.AgentId, req.Body.FlowId)
	if flow == nil {
		return nil, errStatus(http.StatusNotFound, "sign-in session expired or not found; start sign-in again")
	}
	keepFlow := false
	defer func() {
		if !keepFlow {
			flow.close()
		}
	}()

	validated, err := antigravity.ValidateLoginResponse(flow.authURL, req.Body.Code)
	if err != nil {
		// Put the flow back so the user can fix a typo without restarting sign-in.
		flow.timer = time.AfterFunc(authFlowTTL, func() {
			a.authFlows.cancel(flow.agentID, flow.id)
		})
		a.authFlows.put(flow)
		keepFlow = true
		return nil, badRequest(err.Error())
	}

	if _, err := flow.proc.Write([]byte(validated + "\n")); err != nil {
		msg := fmt.Sprintf("could not deliver code to Antigravity: %v", err)
		return gen.SubmitHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
			FlowId:  req.Body.FlowId,
			Status:  gen.HarnessAuthFlowStatusFailed,
			Message: &msg,
		}), nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, authFlowSubmitTimeout)
	defer cancel()

	select {
	case <-flow.proc.Done():
		code, procErr := flow.proc.ExitCode()
		if procErr == nil && code == 0 {
			_ = a.Store.MarkCredentialsInSandbox(context.WithoutCancel(ctx), req.AgentId)
			a.Sessions.Disconnect(req.AgentId)
			a.record(ctx, acc.Row.Agent, audit.HarnessLogin, "oauth-personal", nil)
			msg := "Signed in to Antigravity."
			return gen.SubmitHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
				FlowId:  flow.id,
				Status:  gen.HarnessAuthFlowStatusCompleted,
				Message: &msg,
			}), nil
		}
		detail := flow.output()
		if detail == "" && procErr != nil {
			detail = procErr.Error()
		}
		if detail == "" {
			detail = fmt.Sprintf("Antigravity sign-in failed (exit code %d)", code)
		}
		return gen.SubmitHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
			FlowId:  flow.id,
			Status:  gen.HarnessAuthFlowStatusFailed,
			Message: &detail,
		}), nil

	case <-waitCtx.Done():
		msg := "timed out waiting for Antigravity to finish signing in"
		if out := flow.output(); out != "" {
			msg += ": " + out
		}
		return gen.SubmitHarnessAuth200JSONResponse(gen.HarnessAuthFlow{
			FlowId:  flow.id,
			Status:  gen.HarnessAuthFlowStatusFailed,
			Message: &msg,
		}), nil
	}
}

// CancelHarnessAuth implements gen.StrictServerInterface.
func (a *API) CancelHarnessAuth(ctx context.Context, req gen.CancelHarnessAuthRequestObject) (gen.CancelHarnessAuthResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentAuthLogin); err != nil {
		return nil, err
	}
	flowID := ""
	if req.Body != nil && req.Body.FlowId != nil {
		flowID = *req.Body.FlowId
	}
	a.authFlows.cancel(req.AgentId, flowID)
	return gen.CancelHarnessAuth204Response{}, nil
}
