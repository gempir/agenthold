package acpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Resolver resolves both built-in harnesses and user-configured ACP Store /
// local ACP command harnesses stored in the database.
type Resolver struct {
	Store   *store.Store
	Catalog *Catalog
}

// AdapterFor returns a harness.Adapter for harnessID (checking built-ins first,
// then custom_harnesses in the store).
func (r *Resolver) AdapterFor(ctx context.Context, harnessID string) (*harness.Adapter, bool, error) {
	if ad, ok := harness.Get(harnessID); ok {
		return ad, true, nil
	}
	if r == nil || r.Store == nil {
		return nil, false, nil
	}
	row, err := r.Store.GetCustomHarness(ctx, harnessID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return AdapterFromRow(row), true, nil
}

// Prepare resolves and, for ACP Registry harnesses, installs the harness into
// the agent's persistent home directory inside the sandbox on demand, returning
// the final executable command and merged environment variables.
func (r *Resolver) Prepare(ctx context.Context, ad *harness.Adapter, ts *tunnel.Session, home string, cfg harness.Config, status func(string)) ([]string, map[string]string, error) {
	env := ad.Environment(cfg)
	if ad.Source != "registry" || r == nil || r.Store == nil || r.Catalog == nil {
		return ad.Command, env, nil
	}
	row, err := r.Store.GetCustomHarness(ctx, ad.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load custom harness %q: %w", ad.ID, err)
	}
	// If the user explicitly set a local command override, launch it directly.
	if strings.TrimSpace(row.Command) != "" {
		return ad.Command, env, nil
	}
	agent, err := r.Catalog.Lookup(ctx, row.RegistryID)
	if err != nil {
		return nil, nil, err
	}
	installed, err := EnsureInstalled(ctx, ts, home, agent, row.Distribution, status)
	if err != nil {
		return nil, nil, err
	}
	// Merge registry-declared env first, then user overrides on top.
	merged := map[string]string{}
	maps.Copy(merged, installed.Env)
	maps.Copy(merged, env)
	var extraArgs []string
	_ = json.Unmarshal([]byte(row.ArgsJson), &extraArgs)
	cmd := append(append([]string{}, installed.Command...), extraArgs...)
	return cmd, merged, nil
}

// AdapterFromRow converts a persisted custom_harnesses row into a harness.Adapter.
func AdapterFromRow(row db.CustomHarness) *harness.Adapter {
	var args []string
	_ = json.Unmarshal([]byte(row.ArgsJson), &args)
	var envMap map[string]string
	_ = json.Unmarshal([]byte(row.EnvJson), &envMap)
	var customModels []string
	_ = json.Unmarshal([]byte(row.CustomModelsJson), &customModels)

	var cmd []string
	if strings.TrimSpace(row.Command) != "" {
		cmd = append([]string{strings.TrimSpace(row.Command)}, args...)
	}
	var fallbacks []harness.Model
	for _, m := range customModels {
		m = strings.TrimSpace(m)
		if m != "" {
			fallbacks = append(fallbacks, harness.Model{ID: m, Name: m, Group: row.Name})
		}
	}
	var autoAuth []string
	if strings.TrimSpace(row.AuthMethod) != "" {
		autoAuth = []string{strings.TrimSpace(row.AuthMethod)}
	}
	return &harness.Adapter{
		ID:               row.ID,
		Name:             row.Name,
		Protocol:         harness.ProtocolACP,
		Source:           row.Kind,
		Description:      row.Description,
		Version:          row.RegistryVersion,
		IconURL:          row.IconUrl,
		Command:          cmd,
		Env:              func(harness.Config) map[string]string { return maps.Clone(envMap) },
		AutoAuthenticate: autoAuth,
		FallbackModels:   fallbacks,
		CustomModelsList: customModels,
		CustomModels:     true,
	}
}
