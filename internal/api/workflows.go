package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/workflows"
)

func (a *API) workflowEngine() *workflows.Engine {
	if a.Workflows != nil {
		return a.Workflows
	}
	return workflows.NewEngine(workflows.Config{
		Store: a.Store,
		Agents: &workflows.SessionAgentRunner{
			Store:    a.Store,
			Sessions: a.Sessions,
		},
		Shell: &workflows.TunnelShellRunner{
			Store: a.Store,
			Wake:  a.Wake,
		},
		Now: a.Now,
	})
}

func apiWorkflowDef(def workflows.Definition) gen.WorkflowDefinition {
	steps := make([]gen.WorkflowStepDef, 0, len(def.Steps))
	for _, s := range def.Steps {
		sd := gen.WorkflowStepDef{
			Name: s.Name,
			Kind: gen.WorkflowStepKind(s.Kind),
		}
		if s.AgentID != "" {
			v := s.AgentID
			sd.AgentId = &v
		}
		if s.Prompt != "" {
			v := s.Prompt
			sd.Prompt = &v
		}
		if s.ReuseSession {
			v := true
			sd.ReuseSession = &v
		}
		if s.Command != "" {
			v := s.Command
			sd.Command = &v
		}
		if s.Cwd != "" {
			v := s.Cwd
			sd.Cwd = &v
		}
		if s.AskTitle != "" {
			v := s.AskTitle
			sd.AskTitle = &v
		}
		if len(s.AskChoices) > 0 {
			v := append([]string(nil), s.AskChoices...)
			sd.AskChoices = &v
		}
		if len(s.ContinueOnChoices) > 0 {
			v := append([]string(nil), s.ContinueOnChoices...)
			sd.ContinueOnChoices = &v
		}
		steps = append(steps, sd)
	}
	out := gen.WorkflowDefinition{Steps: steps}
	if def.Schedule != "" {
		v := def.Schedule
		out.Schedule = &v
	}
	if def.Timezone != "" {
		v := def.Timezone
		out.Timezone = &v
	}
	if def.ConcurrencyLimit > 0 {
		v := def.ConcurrencyLimit
		out.ConcurrencyLimit = &v
	}
	if def.WebhookEnabled {
		v := true
		out.WebhookEnabled = &v
	}
	return out
}

func (a *API) apiWorkflow(row db.Workflow, webhookToken string) gen.Workflow {
	def, _ := workflows.ParseDefinition(row.DefinitionJson)
	out := gen.Workflow{
		Id:          row.ID,
		OwnerId:     row.OwnerID,
		Name:        row.Name,
		Description: row.Description,
		Definition:  apiWorkflowDef(def),
		Enabled:     row.Enabled == 1,
		HasWebhook:  def.WebhookEnabled && def.WebhookTokenHash != "",
		CreatedAt:   ts(row.CreatedAt),
		UpdatedAt:   ts(row.UpdatedAt),
	}
	if webhookToken != "" {
		u := a.PublicURL + "/api/v1/workflow-hooks/" + webhookToken
		out.WebhookUrl = &u
	}
	return out
}

func apiWorkflowRun(d workflows.RunDetail) gen.WorkflowRun {
	var inObj map[string]any
	_ = json.Unmarshal([]byte(d.Run.InputJson), &inObj)
	if inObj == nil {
		inObj = map[string]any{}
	}
	var outObj map[string]any
	_ = json.Unmarshal([]byte(d.Run.OutputJson), &outObj)
	if outObj == nil {
		outObj = map[string]any{}
	}
	steps := make([]gen.WorkflowStep, 0, len(d.Steps))
	for _, s := range d.Steps {
		var choices []string
		_ = json.Unmarshal([]byte(s.AskChoicesJson), &choices)
		if choices == nil {
			choices = []string{}
		}
		gs := gen.WorkflowStep{
			Id:         s.ID,
			RunId:      s.RunID,
			StepIndex:  int(s.StepIndex),
			Name:       s.Name,
			Kind:       gen.WorkflowStepKind(s.Kind),
			Status:     gen.WorkflowStepStatus(s.Status),
			AskChoices: choices,
			StartedAt:  tsPtr(s.StartedAt),
			FinishedAt: tsPtr(s.FinishedAt),
		}
		idx := int(s.StepIndex)
		if idx >= 0 && idx < len(d.Definition.Steps) && d.Definition.Steps[idx].AgentID != "" {
			aid := d.Definition.Steps[idx].AgentID
			gs.AgentId = &aid
		}
		if s.SessionID != "" {
			v := s.SessionID
			gs.SessionId = &v
		}
		if s.PromptOrCmd != "" {
			v := s.PromptOrCmd
			gs.PromptOrCmd = &v
		}
		if s.AskTitle != "" {
			v := s.AskTitle
			gs.AskTitle = &v
		}
		if s.AnswerJson != "" {
			var ans workflows.StepAnswer
			if json.Unmarshal([]byte(s.AnswerJson), &ans) == nil {
				if ans.Choice != "" {
					v := ans.Choice
					gs.AnswerChoice = &v
				}
				if ans.Reason != "" {
					v := ans.Reason
					gs.AnswerReason = &v
				}
			}
		}
		if s.AnsweredBy != "" {
			v := s.AnsweredBy
			gs.AnsweredBy = &v
		}
		if s.Output != "" {
			v := s.Output
			gs.Output = &v
		}
		if s.Error != "" {
			v := s.Error
			gs.Error = &v
		}
		steps = append(steps, gs)
	}
	out := gen.WorkflowRun{
		Id:           d.Run.ID,
		WorkflowId:   d.Run.WorkflowID,
		WorkflowName: d.WorkflowName,
		OwnerId:      d.Run.OwnerID,
		Status:       gen.WorkflowRunStatus(d.Run.Status),
		TriggerKind:  gen.WorkflowTriggerKind(d.Run.TriggerKind),
		Input:        inObj,
		Output:       outObj,
		CurrentStep:  int(d.Run.CurrentStep),
		Steps:        steps,
		StartedAt:    tsPtr(d.Run.StartedAt),
		FinishedAt:   tsPtr(d.Run.FinishedAt),
		CreatedAt:    ts(d.Run.CreatedAt),
		UpdatedAt:    ts(d.Run.UpdatedAt),
	}
	if d.Run.Error != "" {
		v := d.Run.Error
		out.Error = &v
	}
	return out
}

func (a *API) parseWorkflowInput(ctx context.Context, userID string, in gen.WorkflowInput, existingDef *workflows.Definition) (name, desc string, def workflows.Definition, enabled bool, rawToken string, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return "", "", def, false, "", badRequest("name must be 1 to 100 characters")
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
		if len(desc) > 1024 {
			return "", "", def, false, "", badRequest("description must be at most 1024 characters")
		}
	}
	enabled = true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	steps := make([]workflows.StepDef, 0, len(in.Definition.Steps))
	for _, s := range in.Definition.Steps {
		sd := workflows.StepDef{
			Name: s.Name,
			Kind: string(s.Kind),
		}
		if s.AgentId != nil {
			sd.AgentID = *s.AgentId
		}
		if s.Prompt != nil {
			sd.Prompt = *s.Prompt
		}
		if s.ReuseSession != nil {
			sd.ReuseSession = *s.ReuseSession
		}
		if s.Command != nil {
			sd.Command = *s.Command
		}
		if s.Cwd != nil {
			sd.Cwd = *s.Cwd
		}
		if s.AskTitle != nil {
			sd.AskTitle = *s.AskTitle
		}
		if s.AskChoices != nil {
			sd.AskChoices = append([]string(nil), (*s.AskChoices)...)
		}
		if s.ContinueOnChoices != nil {
			sd.ContinueOnChoices = append([]string(nil), (*s.ContinueOnChoices)...)
		}
		steps = append(steps, sd)
	}
	def = workflows.Definition{Steps: steps}
	if in.Definition.Schedule != nil {
		def.Schedule = *in.Definition.Schedule
	}
	if in.Definition.Timezone != nil {
		def.Timezone = *in.Definition.Timezone
	}
	if in.Definition.ConcurrencyLimit != nil {
		def.ConcurrencyLimit = *in.Definition.ConcurrencyLimit
	}
	if in.Definition.WebhookEnabled != nil {
		def.WebhookEnabled = *in.Definition.WebhookEnabled
	}

	if err := def.Validate(a.Now()); err != nil {
		return "", "", def, false, "", badRequest(err.Error())
	}

	// Verify caller can prompt every referenced agent.
	checkedAgents := map[string]bool{}
	for _, sd := range def.Steps {
		if sd.AgentID == "" || checkedAgents[sd.AgentID] {
			continue
		}
		if _, err := a.authz.Agent(ctx, userID, sd.AgentID, authz.AgentPrompt); err != nil {
			return "", "", def, false, "", err
		}
		checkedAgents[sd.AgentID] = true
	}

	rotate := in.RotateWebhookToken != nil && *in.RotateWebhookToken
	if def.WebhookEnabled {
		if existingDef != nil && existingDef.WebhookTokenHash != "" && !rotate {
			def.WebhookTokenHash = existingDef.WebhookTokenHash
		} else {
			rawToken = id.Random(24)
			def.WebhookTokenHash = auth.HashToken(rawToken)
		}
	} else {
		def.WebhookTokenHash = ""
	}
	return name, desc, def, enabled, rawToken, nil
}

func workflowErr(err error) error {
	switch {
	case errors.Is(err, workflows.ErrNotFound):
		return errNotFound
	case errors.Is(err, workflows.ErrInvalidDefinition), errors.Is(err, workflows.ErrInvalidAnswer):
		return badRequest(err.Error())
	case errors.Is(err, workflows.ErrConflict):
		return errStatus(http.StatusConflict, err.Error())
	}
	return err
}

// ListWorkflows implements gen.StrictServerInterface.
func (a *API) ListWorkflows(ctx context.Context, _ gen.ListWorkflowsRequestObject) (gen.ListWorkflowsResponseObject, error) {
	rows, err := a.Store.ListUserWorkflows(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListWorkflows200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.apiWorkflow(r, ""))
	}
	return out, nil
}

// CreateWorkflow implements gen.StrictServerInterface.
func (a *API) CreateWorkflow(ctx context.Context, req gen.CreateWorkflowRequestObject) (gen.CreateWorkflowResponseObject, error) {
	name, desc, def, enabled, rawToken, err := a.parseWorkflowInput(ctx, me(ctx).ID, *req.Body, nil)
	if err != nil {
		return nil, err
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	nowUnix := a.Now().Unix()
	row := db.Workflow{
		ID:             id.New("wfl"),
		OwnerID:        me(ctx).ID,
		Name:           name,
		Description:    desc,
		DefinitionJson: string(defJSON),
		Enabled:        b2i(enabled),
		CreatedAt:      nowUnix,
		UpdatedAt:      nowUnix,
	}
	if err := a.Store.CreateWorkflow(ctx, db.CreateWorkflowParams(row)); err != nil {
		return nil, err
	}
	return gen.CreateWorkflow201JSONResponse(a.apiWorkflow(row, rawToken)), nil
}

// UpdateWorkflow implements gen.StrictServerInterface.
func (a *API) UpdateWorkflow(ctx context.Context, req gen.UpdateWorkflowRequestObject) (gen.UpdateWorkflowResponseObject, error) {
	existing, err := a.Store.GetWorkflow(ctx, req.WorkflowId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existing.OwnerID != me(ctx).ID) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	existingDef, _ := workflows.ParseDefinition(existing.DefinitionJson)
	name, desc, def, enabled, rawToken, err := a.parseWorkflowInput(ctx, me(ctx).ID, *req.Body, &existingDef)
	if err != nil {
		return nil, err
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	nowUnix := a.Now().Unix()
	existing.Name = name
	existing.Description = desc
	existing.DefinitionJson = string(defJSON)
	existing.Enabled = b2i(enabled)
	existing.UpdatedAt = nowUnix
	if err := a.Store.UpdateWorkflow(ctx, db.UpdateWorkflowParams{
		Name:           existing.Name,
		Description:    existing.Description,
		DefinitionJson: existing.DefinitionJson,
		Enabled:        existing.Enabled,
		UpdatedAt:      existing.UpdatedAt,
		ID:             existing.ID,
	}); err != nil {
		return nil, err
	}
	return gen.UpdateWorkflow200JSONResponse(a.apiWorkflow(existing, rawToken)), nil
}

// DeleteWorkflow implements gen.StrictServerInterface.
func (a *API) DeleteWorkflow(ctx context.Context, req gen.DeleteWorkflowRequestObject) (gen.DeleteWorkflowResponseObject, error) {
	existing, err := a.Store.GetWorkflow(ctx, req.WorkflowId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existing.OwnerID != me(ctx).ID) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.Store.DeleteWorkflow(ctx, existing.ID); err != nil {
		return nil, err
	}
	return gen.DeleteWorkflow204Response{}, nil
}

// StartWorkflowRun implements gen.StrictServerInterface.
func (a *API) StartWorkflowRun(ctx context.Context, req gen.StartWorkflowRunRequestObject) (gen.StartWorkflowRunResponseObject, error) {
	var input map[string]any
	if req.Body != nil && req.Body.Input != nil {
		input = *req.Body.Input
	}
	detail, err := a.workflowEngine().StartRun(ctx, req.WorkflowId, me(ctx).ID, workflows.TriggerManual, input)
	if err != nil {
		return nil, workflowErr(err)
	}
	return gen.StartWorkflowRun201JSONResponse(apiWorkflowRun(detail)), nil
}

// ListWorkflowRuns implements gen.StrictServerInterface.
func (a *API) ListWorkflowRuns(ctx context.Context, req gen.ListWorkflowRunsRequestObject) (gen.ListWorkflowRunsResponseObject, error) {
	status := "all"
	if req.Params.Status != nil && *req.Params.Status != "" {
		status = string(*req.Params.Status)
	}
	list, err := a.workflowEngine().ListRuns(ctx, me(ctx).ID, status, 100)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListWorkflowRuns200JSONResponse, 0, len(list))
	for _, d := range list {
		out = append(out, apiWorkflowRun(d))
	}
	return out, nil
}

// GetWorkflowRun implements gen.StrictServerInterface.
func (a *API) GetWorkflowRun(ctx context.Context, req gen.GetWorkflowRunRequestObject) (gen.GetWorkflowRunResponseObject, error) {
	d, err := a.workflowEngine().GetRun(ctx, req.RunId, me(ctx).ID)
	if err != nil {
		return nil, workflowErr(err)
	}
	return gen.GetWorkflowRun200JSONResponse(apiWorkflowRun(d)), nil
}

// CancelWorkflowRun implements gen.StrictServerInterface.
func (a *API) CancelWorkflowRun(ctx context.Context, req gen.CancelWorkflowRunRequestObject) (gen.CancelWorkflowRunResponseObject, error) {
	d, err := a.workflowEngine().CancelRun(ctx, req.RunId, me(ctx).ID)
	if err != nil {
		return nil, workflowErr(err)
	}
	return gen.CancelWorkflowRun200JSONResponse(apiWorkflowRun(d)), nil
}

// ResumeWorkflowRun implements gen.StrictServerInterface.
func (a *API) ResumeWorkflowRun(ctx context.Context, req gen.ResumeWorkflowRunRequestObject) (gen.ResumeWorkflowRunResponseObject, error) {
	d, err := a.workflowEngine().ResumeRun(ctx, req.RunId, me(ctx).ID)
	if err != nil {
		return nil, workflowErr(err)
	}
	return gen.ResumeWorkflowRun200JSONResponse(apiWorkflowRun(d)), nil
}

// AnswerWorkflowStep implements gen.StrictServerInterface.
func (a *API) AnswerWorkflowStep(ctx context.Context, req gen.AnswerWorkflowStepRequestObject) (gen.AnswerWorkflowStepResponseObject, error) {
	reason := ""
	if req.Body.Reason != nil {
		reason = *req.Body.Reason
	}
	d, err := a.workflowEngine().AnswerStep(ctx, req.RunId, req.StepId, me(ctx).ID, req.Body.Choice, reason)
	if err != nil {
		return nil, workflowErr(err)
	}
	return gen.AnswerWorkflowStep200JSONResponse(apiWorkflowRun(d)), nil
}

// deliverWorkflowHook handles public POST /api/v1/workflow-hooks/{token}.
func (a *API) deliverWorkflowHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, r, errNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		writeError(w, r, badRequest("could not read the body"))
		return
	}
	if len(body) > maxWebhookBody {
		writeError(w, r, errStatus(http.StatusRequestEntityTooLarge, "the body is larger than 256 KiB"))
		return
	}
	payload := map[string]any{}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			payload = map[string]any{"body": string(body)}
		}
	}
	d, err := a.workflowEngine().TriggerWebhook(ctx, auth.HashToken(token), payload)
	if err != nil {
		writeError(w, r, workflowErr(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId":      d.Run.ID,
		"workflowId": d.Run.WorkflowID,
		"status":     d.Run.Status,
	})
}
