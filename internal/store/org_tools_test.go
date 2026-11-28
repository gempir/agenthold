package store

import (
	"database/sql"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestOrgToolsMigrationAndQueries(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateUser(ctx, db.CreateUserParams{
		ID: "u1", Email: "alice@example.com", Name: "Alice", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// org_tools CRUD.
	if err := s.CreateOrgTool(ctx, db.CreateOrgToolParams{
		ID:               "ot_1",
		Slug:             "linear",
		Name:             "Linear",
		Description:      "Issue tracking",
		Kind:             "executor_app",
		AuthMode:         "user_oauth",
		ConfigJson:       `{"executorCatalogId":"linear"}`,
		EncryptedSecret:  nil,
		SecretHostsJson:  `[]`,
		EnabledByDefault: 1,
		AllowedRolesJson: `["admin","user"]`,
		CreatedBy:        sql.NullString{String: "u1", Valid: true},
		CreatedAt:        100,
		UpdatedAt:        100,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrgToolBySlug(ctx, "linear")
	if err != nil || got.ID != "ot_1" || got.Name != "Linear" || got.EncryptedSecret != nil {
		t.Fatalf("get org tool: %+v %v", got, err)
	}
	if err := s.UpdateOrgTool(ctx, db.UpdateOrgToolParams{
		Slug:             "linear",
		Name:             "Linear Issues",
		Description:      "Updated",
		Kind:             "mcp_http",
		AuthMode:         "org_shared",
		ConfigJson:       `{"url":"https://mcp.linear.app"}`,
		EncryptedSecret:  []byte("cipher"),
		SecretHostsJson:  `["mcp.linear.app"]`,
		EnabledByDefault: 1,
		AllowedRolesJson: `["admin"]`,
		UpdatedAt:        200,
		ID:               "ot_1",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetOrgTool(ctx, "ot_1")
	if err != nil || got.Name != "Linear Issues" || string(got.EncryptedSecret) != "cipher" {
		t.Fatalf("after update: %+v %v", got, err)
	}

	// user_tool_preferences upsert & cascade delete.
	if err := s.UpsertUserToolPreference(ctx, db.UpsertUserToolPreferenceParams{
		UserID: "u1", OrgToolID: "ot_1", Enabled: 0,
	}); err != nil {
		t.Fatal(err)
	}
	prefs, err := s.ListUserToolPreferences(ctx, "u1")
	if err != nil || len(prefs) != 1 || prefs[0].Enabled != 0 {
		t.Fatalf("prefs: %+v %v", prefs, err)
	}
	if err := s.UpsertUserToolPreference(ctx, db.UpsertUserToolPreferenceParams{
		UserID: "u1", OrgToolID: "ot_1", Enabled: 1,
	}); err != nil {
		t.Fatal(err)
	}
	prefs, _ = s.ListUserToolPreferences(ctx, "u1")
	if len(prefs) != 1 || prefs[0].Enabled != 1 {
		t.Fatalf("updated prefs: %+v", prefs)
	}
	n, err := s.DeleteOrgTool(ctx, "ot_1")
	if err != nil || n != 1 {
		t.Fatalf("delete org tool: n=%d err=%v", n, err)
	}
	prefs, _ = s.ListUserToolPreferences(ctx, "u1")
	if len(prefs) != 0 {
		t.Fatalf("expected cascade delete of prefs, got %+v", prefs)
	}

	// org_skills CRUD + default filter.
	if err := s.CreateOrgSkill(ctx, db.CreateOrgSkillParams{
		ID: "osk_1", Name: "code-review", Description: "Review guide", Content: "# Guide", EnabledByDefault: 1, CreatedAt: 10, UpdatedAt: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrgSkill(ctx, db.CreateOrgSkillParams{
		ID: "osk_2", Name: "optional", Description: "Opt", Content: "# Opt", EnabledByDefault: 0, CreatedAt: 10, UpdatedAt: 10,
	}); err != nil {
		t.Fatal(err)
	}
	allSkills, err := s.ListOrgSkills(ctx)
	if err != nil || len(allSkills) != 2 {
		t.Fatalf("all org skills: %+v %v", allSkills, err)
	}
	defSkills, err := s.ListDefaultOrgSkills(ctx)
	if err != nil || len(defSkills) != 1 || defSkills[0].Name != "code-review" {
		t.Fatalf("default org skills: %+v %v", defSkills, err)
	}
	if n, err := s.DeleteOrgSkill(ctx, "osk_2"); err != nil || n != 1 {
		t.Fatalf("delete org skill: n=%d err=%v", n, err)
	}

	// org_provider_logins upsert, get, list, delete.
	if err := s.UpsertOrgProviderLogin(ctx, db.UpsertOrgProviderLoginParams{
		Provider:         "claude-code",
		Kind:             "api_key",
		Label:            "Org Claude",
		EncryptedPayload: []byte("enc1"),
		Enabled:          1,
		UpdatedBy:        sql.NullString{String: "u1", Valid: true},
		UpdatedAt:        50,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertOrgProviderLogin(ctx, db.UpsertOrgProviderLoginParams{
		Provider:         "claude-code",
		Kind:             "gateway",
		Label:            "Org Gateway",
		EncryptedPayload: []byte("enc2"),
		Enabled:          1,
		UpdatedBy:        sql.NullString{String: "u1", Valid: true},
		UpdatedAt:        60,
	}); err != nil {
		t.Fatal(err)
	}
	pl, err := s.GetOrgProviderLogin(ctx, "claude-code")
	if err != nil || pl.Kind != "gateway" || string(pl.EncryptedPayload) != "enc2" {
		t.Fatalf("get org provider: %+v %v", pl, err)
	}
	list, err := s.ListOrgProviderLogins(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list org providers: %+v %v", list, err)
	}
	if n, err := s.DeleteOrgProviderLogin(ctx, "claude-code"); err != nil || n != 1 {
		t.Fatalf("delete org provider: n=%d err=%v", n, err)
	}
}
