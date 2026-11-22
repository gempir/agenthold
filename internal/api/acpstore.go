package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gempir/agenthold/internal/acpstore"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

const (
	maxCustomHarnessName   = 120
	maxCustomHarnessDesc   = 1024
	maxCustomHarnessCmd    = 512
	maxCustomHarnessArgs   = 64
	maxCustomHarnessEnv    = 64
	maxCustomHarnessModels = 64
)

var customEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ownerHasHarness reports whether harnessID is a visible built-in harness or
// a custom harness owned by userID.
func (a *API) ownerHasHarness(ctx context.Context, userID, harnessID string) (bool, error) {
	if ad, ok := harness.Get(harnessID); ok {
		if ad.Hidden && !a.ShowTestHarness {
			return false, nil
		}
		return true, nil
	}
	row, err := a.Store.GetCustomHarness(ctx, harnessID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.OwnerID == userID, nil
}

// SearchAcpRegistry implements gen.StrictServerInterface.
func (a *API) SearchAcpRegistry(ctx context.Context, req gen.SearchAcpRegistryRequestObject) (gen.SearchAcpRegistryResponseObject, error) {
	if a.ACPStore == nil {
		return nil, errStatus(http.StatusServiceUnavailable, "ACP Registry is not configured")
	}
	q := ""
	if req.Params.Q != nil {
		q = strings.TrimSpace(*req.Params.Q)
	}
	limit := 30
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	hits, ver, err := a.ACPStore.Search(ctx, q, limit, "")
	if err != nil {
		return nil, errStatus(http.StatusBadGateway, fmt.Sprintf("could not fetch ACP Registry: %v", err))
	}
	addedByReg := map[string]string{}
	if rows, err := a.Store.ListCustomHarnessesByOwner(ctx, me(ctx).ID); err == nil {
		for _, r := range rows {
			if r.Kind == "registry" && r.RegistryID != "" {
				if _, exists := addedByReg[r.RegistryID]; !exists {
					addedByReg[r.RegistryID] = r.ID
				}
			}
		}
	}
	out := gen.AcpRegistrySearchResponse{
		Version: ver,
		Agents:  make([]gen.AcpRegistryAgent, 0, len(hits)),
	}
	for _, h := range hits {
		item := gen.AcpRegistryAgent{
			Id:            h.Agent.ID,
			Name:          h.Agent.Name,
			Version:       h.Agent.Version,
			Description:   h.Agent.Description,
			Distributions: h.Distributions,
			License:       opt(h.Agent.License),
			Website:       opt(h.Agent.Website),
			Repository:    opt(h.Agent.Repository),
			Icon:          opt(h.Agent.Icon),
			BuiltinId:     opt(h.BuiltinID),
		}
		if len(h.Agent.Authors) > 0 {
			authors := append([]string{}, h.Agent.Authors...)
			item.Authors = &authors
		}
		if id, ok := addedByReg[h.Agent.ID]; ok {
			item.AddedId = &id
		}
		out.Agents = append(out.Agents, item)
	}
	return gen.SearchAcpRegistry200JSONResponse(out), nil
}

// ListCustomHarnesses implements gen.StrictServerInterface.
func (a *API) ListCustomHarnesses(ctx context.Context, _ gen.ListCustomHarnessesRequestObject) (gen.ListCustomHarnessesResponseObject, error) {
	rows, err := a.Store.ListCustomHarnessesByOwner(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListCustomHarnesses200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiCustomHarness(r))
	}
	return out, nil
}

// CreateCustomHarness implements gen.StrictServerInterface.
func (a *API) CreateCustomHarness(ctx context.Context, req gen.CreateCustomHarnessRequestObject) (gen.CreateCustomHarnessResponseObject, error) {
	u := me(ctx)
	b := req.Body
	if b == nil {
		return nil, badRequest("request body is required")
	}
	kind := string(b.Kind)
	if kind != "registry" && kind != "local" {
		return nil, badRequest("kind must be registry or local")
	}

	var args []string
	if b.Args != nil {
		var err error
		if args, err = cleanStringList("args", *b.Args, maxCustomHarnessArgs, 512); err != nil {
			return nil, err
		}
	}
	envMap := map[string]string{}
	if b.Env != nil {
		var err error
		if envMap, err = cleanEnvMap(*b.Env); err != nil {
			return nil, err
		}
	}
	var customModels []string
	if b.CustomModels != nil {
		var err error
		if customModels, err = cleanModelList(*b.CustomModels); err != nil {
			return nil, err
		}
	}
	authMethod := strings.TrimSpace(deref(b.AuthMethod))
	if len(authMethod) > 128 {
		return nil, badRequest("authMethod is too long")
	}
	command := strings.TrimSpace(deref(b.Command))
	if len(command) > maxCustomHarnessCmd || strings.ContainsAny(command, "\r\n") {
		return nil, badRequest("invalid command")
	}
	dist := ""
	if b.Distribution != nil {
		dist = string(*b.Distribution)
	}
	if dist != "" && dist != "auto" && dist != "binary" && dist != "npx" && dist != "uvx" {
		return nil, badRequest("distribution must be auto, binary, npx, or uvx")
	}
	if dist == "auto" {
		dist = ""
	}

	now := a.Now().Unix()
	params := db.CreateCustomHarnessParams{
		OwnerID:          u.ID,
		Kind:             kind,
		Distribution:     dist,
		Command:          command,
		ArgsJson:         mustJSON(args, "[]"),
		EnvJson:          mustJSON(envMap, "{}"),
		CustomModelsJson: mustJSON(customModels, "[]"),
		AuthMethod:       authMethod,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	switch kind {
	case "registry":
		regID := strings.TrimSpace(deref(b.RegistryId))
		if regID == "" {
			return nil, badRequest("registryId is required when kind is registry")
		}
		if a.ACPStore == nil {
			return nil, errStatus(http.StatusServiceUnavailable, "ACP Registry is not configured")
		}
		agent, err := a.ACPStore.Lookup(ctx, regID)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		if dist != "" && !hasDistribution(agent, dist) {
			return nil, badRequest(fmt.Sprintf("%s does not publish a %s distribution", agent.Name, dist))
		}
		params.ID = id.New("acp")
		params.RegistryID = agent.ID
		params.RegistryVersion = agent.Version
		params.Name = strings.TrimSpace(deref(b.Name))
		if params.Name == "" {
			params.Name = agent.Name
		}
		params.Description = strings.TrimSpace(deref(b.Description))
		if params.Description == "" {
			params.Description = agent.Description
		}
		params.IconUrl = agent.Icon
		params.WebsiteUrl = agent.Website
		params.RepositoryUrl = agent.Repository
	case "local":
		params.ID = id.New("loc")
		params.Name = strings.TrimSpace(deref(b.Name))
		if params.Name == "" {
			return nil, badRequest("name is required for a local ACP harness")
		}
		if command == "" {
			return nil, badRequest("command is required for a local ACP harness")
		}
		params.Description = strings.TrimSpace(deref(b.Description))
	}

	if len(params.Name) > maxCustomHarnessName {
		return nil, badRequest(fmt.Sprintf("name must be at most %d characters", maxCustomHarnessName))
	}
	if len(params.Description) > maxCustomHarnessDesc {
		return nil, badRequest(fmt.Sprintf("description must be at most %d characters", maxCustomHarnessDesc))
	}

	if err := a.Store.CreateCustomHarness(ctx, params); err != nil {
		return nil, err
	}
	row, err := a.Store.GetCustomHarness(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	return gen.CreateCustomHarness201JSONResponse(apiCustomHarness(row)), nil
}

// UpdateCustomHarness implements gen.StrictServerInterface.
func (a *API) UpdateCustomHarness(ctx context.Context, req gen.UpdateCustomHarnessRequestObject) (gen.UpdateCustomHarnessResponseObject, error) {
	u := me(ctx)
	row, err := a.Store.GetCustomHarness(ctx, req.HarnessId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.OwnerID != u.ID) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("request body is required")
	}
	if b.Name != nil {
		name := strings.TrimSpace(*b.Name)
		if name == "" || len(name) > maxCustomHarnessName {
			return nil, badRequest(fmt.Sprintf("name must be 1 to %d characters", maxCustomHarnessName))
		}
		row.Name = name
	}
	if b.Description != nil {
		desc := strings.TrimSpace(*b.Description)
		if len(desc) > maxCustomHarnessDesc {
			return nil, badRequest(fmt.Sprintf("description must be at most %d characters", maxCustomHarnessDesc))
		}
		row.Description = desc
	}
	if b.Command != nil {
		cmd := strings.TrimSpace(*b.Command)
		if row.Kind == "local" && cmd == "" {
			return nil, badRequest("command is required for a local ACP harness")
		}
		if len(cmd) > maxCustomHarnessCmd || strings.ContainsAny(cmd, "\r\n") {
			return nil, badRequest("invalid command")
		}
		row.Command = cmd
	}
	if b.Distribution != nil {
		dist := string(*b.Distribution)
		if dist != "" && dist != "auto" && dist != "binary" && dist != "npx" && dist != "uvx" {
			return nil, badRequest("distribution must be auto, binary, npx, or uvx")
		}
		if dist == "auto" {
			dist = ""
		}
		row.Distribution = dist
	}
	if b.Args != nil {
		args, err := cleanStringList("args", *b.Args, maxCustomHarnessArgs, 512)
		if err != nil {
			return nil, err
		}
		row.ArgsJson = mustJSON(args, "[]")
	}
	if b.Env != nil {
		envMap, err := cleanEnvMap(*b.Env)
		if err != nil {
			return nil, err
		}
		row.EnvJson = mustJSON(envMap, "{}")
	}
	if b.CustomModels != nil {
		models, err := cleanModelList(*b.CustomModels)
		if err != nil {
			return nil, err
		}
		row.CustomModelsJson = mustJSON(models, "[]")
	}
	if b.AuthMethod != nil {
		am := strings.TrimSpace(*b.AuthMethod)
		if len(am) > 128 {
			return nil, badRequest("authMethod is too long")
		}
		row.AuthMethod = am
	}

	row.UpdatedAt = a.Now().Unix()
	if err := a.Store.UpdateCustomHarness(ctx, db.UpdateCustomHarnessParams{
		Name:             row.Name,
		Description:      row.Description,
		RegistryVersion:  row.RegistryVersion,
		Distribution:     row.Distribution,
		Command:          row.Command,
		ArgsJson:         row.ArgsJson,
		EnvJson:          row.EnvJson,
		IconUrl:          row.IconUrl,
		WebsiteUrl:       row.WebsiteUrl,
		RepositoryUrl:    row.RepositoryUrl,
		CustomModelsJson: row.CustomModelsJson,
		AuthMethod:       row.AuthMethod,
		UpdatedAt:        row.UpdatedAt,
		ID:               row.ID,
		OwnerID:          u.ID,
	}); err != nil {
		return nil, err
	}

	// Disconnect running harness processes on agents using this custom harness so
	// the next turn picks up the updated command/args/env.
	if agents, err := a.Store.ListUserAgents(ctx, u.ID); err == nil {
		for _, ag := range agents {
			if ag.Agent.Harness == row.ID {
				a.Sessions.Disconnect(ag.Agent.ID)
			}
		}
	}
	return gen.UpdateCustomHarness200JSONResponse(apiCustomHarness(row)), nil
}

// DeleteCustomHarness implements gen.StrictServerInterface.
func (a *API) DeleteCustomHarness(ctx context.Context, req gen.DeleteCustomHarnessRequestObject) (gen.DeleteCustomHarnessResponseObject, error) {
	u := me(ctx)
	row, err := a.Store.GetCustomHarness(ctx, req.HarnessId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.OwnerID != u.ID) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	n, err := a.Store.CountAgentsByHarness(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errStatus(http.StatusConflict, "this harness is still used by one or more agents; switch them to another harness first")
	}
	if _, err := a.Store.DeleteCustomHarness(ctx, db.DeleteCustomHarnessParams{ID: row.ID, OwnerID: u.ID}); err != nil {
		return nil, err
	}
	return gen.DeleteCustomHarness204Response{}, nil
}

func apiCustomHarness(row db.CustomHarness) gen.CustomHarness {
	args := []string{}
	_ = json.Unmarshal([]byte(row.ArgsJson), &args)
	envMap := map[string]string{}
	_ = json.Unmarshal([]byte(row.EnvJson), &envMap)
	models := []string{}
	_ = json.Unmarshal([]byte(row.CustomModelsJson), &models)

	return gen.CustomHarness{
		Id:              row.ID,
		Kind:            gen.CustomHarnessKind(row.Kind),
		Name:            row.Name,
		Description:     row.Description,
		RegistryId:      opt(row.RegistryID),
		RegistryVersion: opt(row.RegistryVersion),
		Distribution:    opt(row.Distribution),
		Command:         opt(row.Command),
		Args:            args,
		Env:             envMap,
		IconUrl:         opt(row.IconUrl),
		WebsiteUrl:      opt(row.WebsiteUrl),
		RepositoryUrl:   opt(row.RepositoryUrl),
		CustomModels:    models,
		AuthMethod:      opt(row.AuthMethod),
		CreatedAt:       ts(row.CreatedAt),
		UpdatedAt:       ts(row.UpdatedAt),
	}
}

func hasDistribution(agent acpstore.Agent, dist string) bool {
	for _, d := range acpstore.SupportedDistributions(agent, "") {
		if d == dist {
			return true
		}
	}
	return false
}

func cleanStringList(field string, in []string, maxItems, maxLen int) ([]string, error) {
	if len(in) > maxItems {
		return nil, badRequest(fmt.Sprintf("%s has too many entries (max %d)", field, maxItems))
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if len(s) > maxLen || strings.ContainsAny(s, "\r\n") {
			return nil, badRequest(fmt.Sprintf("invalid %s entry", field))
		}
		out = append(out, s)
	}
	return out, nil
}

func cleanEnvMap(in map[string]string) (map[string]string, error) {
	if len(in) > maxCustomHarnessEnv {
		return nil, badRequest(fmt.Sprintf("env has too many entries (max %d)", maxCustomHarnessEnv))
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if !customEnvKeyPattern.MatchString(k) || len(k) > 128 {
			return nil, badRequest(fmt.Sprintf("invalid environment variable name %q", k))
		}
		if len(v) > 4096 || strings.ContainsAny(v, "\x00") {
			return nil, badRequest(fmt.Sprintf("invalid value for environment variable %q", k))
		}
		out[k] = v
	}
	return out, nil
}

func cleanModelList(in []string) ([]string, error) {
	if len(in) > maxCustomHarnessModels {
		return nil, badRequest(fmt.Sprintf("customModels has too many entries (max %d)", maxCustomHarnessModels))
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !validModelID(m) {
			return nil, badRequest(fmt.Sprintf("invalid custom model id %q", m))
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

func mustJSON(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}
