package tools

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

func TestInstallSkillsSkipsEmptyManifestWrite(t *testing.T) {
	ctx := t.Context()
	hub := tunnel.NewHub(func(context.Context, string, string) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(hub)
	defer srv.Close()
	home := t.TempDir()
	d, err := sandboxd.New(sandboxd.Config{
		Boot:    sandboxd.Boot{AgentID: "ag_skills", Token: "tok", TunnelURL: "ws" + strings.TrimPrefix(srv.URL, "http")},
		Home:    home,
		Version: "test",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = d.Run(dctx); close(done) }()
	defer func() { cancel(); <-done }()

	ts, err := hub.Wait(ctx, "ag_skills")
	if err != nil {
		t.Fatal(err)
	}

	// With no skills and no prior manifest, nothing is written.
	if err := InstallSkills(ctx, ts, ".claude/skills", nil); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(home, ".claude/skills", manifestName)
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("expected no manifest file when no skills are attached, got err=%v", err)
	}

	// Installing a skill writes SKILL.md and the manifest.
	if err := InstallSkills(ctx, ts, ".claude/skills", []db.Skill{{Name: "hello", Description: "Hi", Content: "Body"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/hello/SKILL.md")); err != nil {
		t.Fatalf("skill not written: %v", err)
	}

	// Removing all skills deletes the previously managed skill and updates the manifest.
	if err := InstallSkills(ctx, ts, ".claude/skills", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/hello")); !os.IsNotExist(err) {
		t.Fatalf("removed skill still exists: %v", err)
	}
}
