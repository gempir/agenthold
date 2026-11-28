package scripts_test

import (
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/scripts"
)

func TestParse(t *testing.T) {
	c, err := scripts.Parse([]byte(`{
		"$schema": "https://t3.codes/schema/t3.json",
		"iconPath": "assets/logo.svg",
		"defaultThreadEnvMode": "worktree",
		"worktreeSubmodules": "top-level",
		"scripts": [
			{"name": "Setup", "command": "pnpm install", "icon": "configure", "runOnWorktreeCreate": true, "async": false},
			{"name": "Dev", "command": "pnpm dev", "previewUrl": "http://localhost:3000", "autoOpenPreview": true, "stopOnSettle": true},
			{"name": "Clean", "command": "cargo clean", "icon": "nope", "runOnSettle": true},
			{"name": "dev", "command": "other"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.IconPath != "assets/logo.svg" || c.DefaultThreadEnvMode != scripts.EnvWorktree || len(c.Scripts) != 4 {
		t.Fatalf("config %+v", c)
	}
	setup, dev, clean := c.Scripts[0], c.Scripts[1], c.Scripts[2]
	if setup.ID != "setup" || setup.IsAsync() || !setup.RunOnWorktreeCreate || setup.LongRunning() || setup.Icon != "configure" {
		t.Fatalf("setup %+v", setup)
	}
	if !dev.LongRunning() || !dev.IsAsync() || dev.PreviewURL != "http://localhost:3000" || !dev.AutoOpenPreview || dev.Icon != "play" {
		t.Fatalf("dev %+v", dev)
	}
	if clean.Icon != "play" || !clean.RunOnSettle {
		t.Fatalf("clean %+v", clean)
	}
	if c.Scripts[3].ID != "dev-2" {
		t.Fatalf("duplicate id %q", c.Scripts[3].ID)
	}
	if s, ok := c.Find("dev-2"); !ok || s.Command != "other" {
		t.Fatalf("find by id %+v", s)
	}
	if s, ok := c.Find("CLEAN"); !ok || s.ID != "clean" {
		t.Fatalf("find by name %+v", s)
	}
	if _, ok := c.Find("missing"); ok {
		t.Fatal("found a missing script")
	}

	for _, bad := range []string{
		`{`, `{"scripts": [{"name": "", "command": "x"}]}`, `{"scripts": [{"name": "x", "command": " "}]}`,
		`{"defaultThreadEnvMode": "cloud"}`, `{"scripts": "x"}`,
		`{"scripts": [` + strings.Repeat(`{"name":"a","command":"b"},`, 50) + `{"name":"a","command":"b"}]}`,
	} {
		if _, err := scripts.Parse([]byte(bad)); err == nil {
			t.Fatalf("parsed %s", bad)
		}
	}
	if c, err := scripts.Parse([]byte(`{}`)); err != nil || c.Scripts == nil || len(c.Scripts) != 0 {
		t.Fatalf("empty %+v %v", c, err)
	}
}

func TestServiceName(t *testing.T) {
	for _, name := range []string{"Dev", "A very long script name that goes on and on", "!!!", "Ünïcode Dev Server 2"} {
		s := scripts.Script{Name: name, ID: scripts.Slug(name)}
		n := scripts.ServiceName("projects/app", s)
		if !sandboxd.ValidServiceName(n) {
			t.Fatalf("%q: invalid service name %q", name, n)
		}
		if n == scripts.ServiceName("projects/other", s) {
			t.Fatalf("%q: same service for two directories", name)
		}
		if n != scripts.ServiceName("projects/app/", s) {
			t.Fatalf("%q: the name depends on a trailing slash", name)
		}
	}
	if got := scripts.Slug("Run Tests (CI)"); got != "run-tests-ci" {
		t.Fatalf("slug %q", got)
	}
}
