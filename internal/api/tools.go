package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tools"
)

var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func apiMCPServer(s db.McpServer) gen.MCPServer {
	d := tools.Decode(s)
	out := gen.MCPServer{
		Id: s.ID, OwnerId: s.OwnerID, Name: s.Name, Transport: gen.MCPServerTransport(s.Transport),
		Command: s.Command, Args: d.ArgList, Env: d.EnvMap, Url: s.Url, Headers: d.HeaderMap,
		Scope: gen.MCPServerScopeLibrary, SecretRefs: d.Refs(), CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt),
	}
	if out.Args == nil {
		out.Args = []string{}
	}
	if out.SecretRefs == nil {
		out.SecretRefs = []string{}
	}
	sort.Strings(out.SecretRefs)
	if s.AgentID != "" {
		out.Scope = gen.MCPServerScopeAgent
		out.AgentId = &s.AgentID
	}
	return out
}

// mcpFromInput validates input and checks that every referenced secret
// exists among ownerID's secrets (servers only use their owner's secrets).
func (a *API) mcpFromInput(ctx context.Context, ownerID string, in gen.MCPServerInput) (db.McpServer, error) {
	s := db.McpServer{Name: strings.TrimSpace(in.Name), Transport: string(in.Transport), Args: "[]", Env: "{}", Headers: "{}"}
	if !mcpNameRe.MatchString(s.Name) {
		return s, badRequest("name must be 1 to 64 letters, digits, dashes or underscores")
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	switch s.Transport {
	case "stdio":
		s.Command = str(in.Command)
		if s.Command == "" {
			return s, badRequest("command is required for stdio servers")
		}
		if in.Args != nil {
			b, _ := json.Marshal(*in.Args)
			s.Args = string(b)
		}
		if in.Env != nil {
			b, _ := json.Marshal(*in.Env)
			s.Env = string(b)
		}
	case "http":
		s.Url = str(in.Url)
		u, err := url.Parse(s.Url)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return s, badRequest("url must be an http(s) URL")
		}
		if in.Headers != nil {
			for k := range *in.Headers {
				if k == "" || strings.ContainsAny(k, " :\r\n") {
					return s, badRequest("invalid header name")
				}
			}
			b, _ := json.Marshal(*in.Headers)
			s.Headers = string(b)
		}
	default:
		return s, badRequest("transport must be stdio or http")
	}
	d := tools.Decode(s)
	_, err := tools.Secrets(ctx, a.Store, ownerID, []tools.Server{d})
	if errors.Is(err, tools.ErrUnknownSecret) {
		return s, badRequest(err.Error())
	}
	if err != nil {
		return s, err
	}
	s.OwnerID = ownerID
	return s, nil
}

func (a *API) insertMCP(ctx context.Context, s db.McpServer) (gen.MCPServer, error) {
	now := a.Now().Unix()
	s.ID, s.CreatedAt, s.UpdatedAt = id.New("mcp"), now, now
	if err := a.Store.CreateMCPServer(ctx, db.CreateMCPServerParams(s)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return gen.MCPServer{}, errStatus(http.StatusConflict, "a server with this name already exists")
		}
		return gen.MCPServer{}, err
	}
	a.Egress.Invalidate()
	return apiMCPServer(s), nil
}

// ListMCPServers implements gen.StrictServerInterface.
func (a *API) ListMCPServers(ctx context.Context, _ gen.ListMCPServersRequestObject) (gen.ListMCPServersResponseObject, error) {
	rows, err := a.Store.ListUserMCPServers(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListMCPServers200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiMCPServer(r))
	}
	return out, nil
}

// CreateMCPServer implements gen.StrictServerInterface.
func (a *API) CreateMCPServer(ctx context.Context, req gen.CreateMCPServerRequestObject) (gen.CreateMCPServerResponseObject, error) {
	s, err := a.mcpFromInput(ctx, me(ctx).ID, *req.Body)
	if err != nil {
		return nil, err
	}
	out, err := a.insertMCP(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.CreateMCPServer201JSONResponse(out), nil
}

// ListAgentMCPServers implements gen.StrictServerInterface.
func (a *API) ListAgentMCPServers(ctx context.Context, req gen.ListAgentMCPServersRequestObject) (gen.ListAgentMCPServersResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentMCPServers(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListAgentMCPServers200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiMCPServer(r))
	}
	return out, nil
}

// CreateAgentMCPServer implements gen.StrictServerInterface.
func (a *API) CreateAgentMCPServer(ctx context.Context, req gen.CreateAgentMCPServerRequestObject) (gen.CreateAgentMCPServerResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	s, err := a.mcpFromInput(ctx, ag.OwnerID, *req.Body)
	if err != nil {
		return nil, err
	}
	s.AgentID = ag.ID
	out, err := a.insertMCP(ctx, s)
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.MCPAdd, s.Name, map[string]any{"transport": s.Transport})
	return gen.CreateAgentMCPServer201JSONResponse(out), nil
}

// loadMCP returns a server the caller may change.
func (a *API) loadMCP(ctx context.Context, serverID string) (db.McpServer, error) {
	s, err := a.Store.GetMCPServer(ctx, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return s, errNotFound
	}
	if err != nil {
		return s, err
	}
	if s.AgentID != "" {
		_, err = a.authz.Agent(ctx, me(ctx).ID, s.AgentID, authz.AgentConfigure)
	} else if s.OwnerID != me(ctx).ID {
		err = errNotFound // other users' libraries are invisible
	}
	return s, err
}

// UpdateMCPServer implements gen.StrictServerInterface.
func (a *API) UpdateMCPServer(ctx context.Context, req gen.UpdateMCPServerRequestObject) (gen.UpdateMCPServerResponseObject, error) {
	cur, err := a.loadMCP(ctx, req.ServerId)
	if err != nil {
		return nil, err
	}
	s, err := a.mcpFromInput(ctx, cur.OwnerID, *req.Body)
	if err != nil {
		return nil, err
	}
	s.ID, s.AgentID, s.CreatedAt, s.UpdatedAt = cur.ID, cur.AgentID, cur.CreatedAt, a.Now().Unix()
	if err := a.Store.UpdateMCPServer(ctx, db.UpdateMCPServerParams{
		Name: s.Name, Transport: s.Transport, Command: s.Command, Args: s.Args, Env: s.Env, Url: s.Url, Headers: s.Headers, UpdatedAt: s.UpdatedAt, ID: s.ID,
	}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errStatus(http.StatusConflict, "a server with this name already exists")
		}
		return nil, err
	}
	a.Egress.Invalidate()
	return gen.UpdateMCPServer200JSONResponse(apiMCPServer(s)), nil
}

// DeleteMCPServer implements gen.StrictServerInterface.
func (a *API) DeleteMCPServer(ctx context.Context, req gen.DeleteMCPServerRequestObject) (gen.DeleteMCPServerResponseObject, error) {
	s, err := a.loadMCP(ctx, req.ServerId)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteMCPServer(ctx, s.ID); err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	return gen.DeleteMCPServer204Response{}, nil
}

// AttachMCPServer implements gen.StrictServerInterface.
func (a *API) AttachMCPServer(ctx context.Context, req gen.AttachMCPServerRequestObject) (gen.AttachMCPServerResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	s, err := a.Store.GetMCPServer(ctx, req.ServerId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (s.OwnerID != acc.Row.Agent.OwnerID || s.AgentID != "")) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := a.Store.AttachMCPServer(ctx, db.AttachMCPServerParams{AgentID: req.AgentId, ServerID: s.ID}); err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	a.record(ctx, acc.Row.Agent, audit.MCPAttach, s.Name, nil)
	return gen.AttachMCPServer204Response{}, nil
}

// DetachMCPServer implements gen.StrictServerInterface.
func (a *API) DetachMCPServer(ctx context.Context, req gen.DetachMCPServerRequestObject) (gen.DetachMCPServerResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	s, err := a.Store.GetMCPServer(ctx, req.ServerId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if s.AgentID == req.AgentId {
		err = a.Store.DeleteMCPServer(ctx, s.ID)
	} else {
		var n int64
		n, err = a.Store.DetachMCPServer(ctx, db.DetachMCPServerParams{AgentID: req.AgentId, ServerID: s.ID})
		if err == nil && n == 0 {
			return nil, errNotFound
		}
	}
	if err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	a.record(ctx, acc.Row.Agent, audit.MCPDetach, s.Name, nil)
	return gen.DetachMCPServer204Response{}, nil
}

func apiSkill(s db.Skill) gen.Skill {
	return gen.Skill{Id: s.ID, OwnerId: s.OwnerID, Name: s.Name, Description: s.Description, Content: s.Content, CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt)}
}

func validSkill(in gen.SkillInput) (db.Skill, error) {
	s := db.Skill{Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Content: in.Content}
	if !tools.SkillName.MatchString(s.Name) {
		return s, badRequest("name must be lowercase letters, digits and dashes (up to 64)")
	}
	if s.Description == "" || len(s.Description) > 1024 {
		return s, badRequest("description must be 1 to 1024 characters")
	}
	if strings.TrimSpace(s.Content) == "" || len(s.Content) > 256*1024 {
		return s, badRequest("content must be 1 byte to 256 KiB")
	}
	return s, nil
}

// ListSkills implements gen.StrictServerInterface.
func (a *API) ListSkills(ctx context.Context, _ gen.ListSkillsRequestObject) (gen.ListSkillsResponseObject, error) {
	rows, err := a.Store.ListUserSkills(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSkills200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiSkill(r))
	}
	return out, nil
}

// CreateSkill implements gen.StrictServerInterface.
func (a *API) CreateSkill(ctx context.Context, req gen.CreateSkillRequestObject) (gen.CreateSkillResponseObject, error) {
	s, err := validSkill(*req.Body)
	if err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	s.ID, s.OwnerID, s.CreatedAt, s.UpdatedAt = id.New("skl"), me(ctx).ID, now, now
	if err := a.Store.CreateSkill(ctx, db.CreateSkillParams(s)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errStatus(http.StatusConflict, "a skill with this name already exists")
		}
		return nil, err
	}
	return gen.CreateSkill201JSONResponse(apiSkill(s)), nil
}

// loadSkill returns one of the caller's skills.
func (a *API) loadSkill(ctx context.Context, skillID string) (db.Skill, error) {
	s, err := a.Store.GetSkill(ctx, skillID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.OwnerID != me(ctx).ID) {
		return s, errNotFound
	}
	return s, err
}

// UpdateSkill implements gen.StrictServerInterface.
func (a *API) UpdateSkill(ctx context.Context, req gen.UpdateSkillRequestObject) (gen.UpdateSkillResponseObject, error) {
	cur, err := a.loadSkill(ctx, req.SkillId)
	if err != nil {
		return nil, err
	}
	s, err := validSkill(*req.Body)
	if err != nil {
		return nil, err
	}
	s.ID, s.OwnerID, s.CreatedAt, s.UpdatedAt = cur.ID, cur.OwnerID, cur.CreatedAt, a.Now().Unix()
	if err := a.Store.UpdateSkill(ctx, db.UpdateSkillParams{Name: s.Name, Description: s.Description, Content: s.Content, UpdatedAt: s.UpdatedAt, ID: s.ID}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errStatus(http.StatusConflict, "a skill with this name already exists")
		}
		return nil, err
	}
	return gen.UpdateSkill200JSONResponse(apiSkill(s)), nil
}

// DeleteSkill implements gen.StrictServerInterface.
func (a *API) DeleteSkill(ctx context.Context, req gen.DeleteSkillRequestObject) (gen.DeleteSkillResponseObject, error) {
	s, err := a.loadSkill(ctx, req.SkillId)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteSkill(ctx, s.ID); err != nil {
		return nil, err
	}
	return gen.DeleteSkill204Response{}, nil
}

// ListAgentSkills implements gen.StrictServerInterface.
func (a *API) ListAgentSkills(ctx context.Context, req gen.ListAgentSkillsRequestObject) (gen.ListAgentSkillsResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentSkills(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListAgentSkills200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiSkill(r))
	}
	return out, nil
}

// AttachSkill implements gen.StrictServerInterface.
func (a *API) AttachSkill(ctx context.Context, req gen.AttachSkillRequestObject) (gen.AttachSkillResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	s, err := a.Store.GetSkill(ctx, req.SkillId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.OwnerID != acc.Row.Agent.OwnerID) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := a.Store.AttachSkill(ctx, db.AttachSkillParams{AgentID: req.AgentId, SkillID: s.ID}); err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.SkillAttach, s.Name, nil)
	return gen.AttachSkill204Response{}, nil
}

// DetachSkill implements gen.StrictServerInterface.
func (a *API) DetachSkill(ctx context.Context, req gen.DetachSkillRequestObject) (gen.DetachSkillResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.DetachSkill(ctx, db.DetachSkillParams{AgentID: req.AgentId, SkillID: req.SkillId})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.record(ctx, acc.Row.Agent, audit.SkillDetach, req.SkillId, nil)
	return gen.DetachSkill204Response{}, nil
}
