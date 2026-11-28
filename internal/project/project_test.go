package project

import (
	"errors"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Web Shop":     "web-shop",
		"  ../x  ":     "x",
		"Ünïcode!!":    "n-code",
		"":             "project",
		"a.b_c-d":      "a.b_c-d",
		"---":          "project",
		"UPPER lower ": "upper-lower",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanSources(t *testing.T) {
	got, err := CleanSources([]SourceInput{
		{URL: "https://github.com/o/api.git"},
		{URL: "git@github.com:o/web"},
		{Kind: KindFolder, Name: "notes"},
		{Path: "code/tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceInput{
		{Name: "api", Kind: KindRepo, URL: "https://github.com/o/api.git"},
		{Name: "web", Kind: KindRepo, URL: "git@github.com:o/web"},
		{Name: "notes", Kind: KindFolder},
		{Name: "tool", Kind: KindFolder, Path: "code/tool"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, bad := range [][]SourceInput{
		{{Kind: KindRepo, Name: "x"}},
		{{Name: "x"}, {Name: "X"}},
		{{Name: ".threads"}},
		{{Name: "x", Path: "/abs"}},
		{{Name: "x", Path: "../up"}},
		{{Name: "x", Kind: KindFolder, URL: "https://x"}},
		{{URL: "-oops"}},
		{{Name: "x", DefaultBranch: "a..b"}},
		{{Name: "x", Kind: "svn"}},
	} {
		if _, err := CleanSources(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanSources(%+v) = %v", bad, err)
		}
	}
}

func TestPaths(t *testing.T) {
	p := db.Project{Slug: "shop"}
	if got := MainPath(p, db.ProjectSource{Name: "api"}); got != "projects/shop/api" {
		t.Error(got)
	}
	if got := MainPath(p, db.ProjectSource{Name: "api", Path: "code/api"}); got != "code/api" {
		t.Error(got)
	}
	if got := ThreadBranch("ses_abcdefghijklmnop"); got != "agenthold/abcdefghij" {
		t.Error(got)
	}
	if got := ThreadCwd(p, db.AgentSession{ProjectBranch: "agenthold/abc"}); got != "projects/shop/.threads/abc" {
		t.Error(got)
	}
	if got := ThreadCwd(p, db.AgentSession{}); got != "projects/shop" {
		t.Error(got)
	}
	if got := relPath("projects/shop", "code/api"); got != "../../code/api" {
		t.Error(got)
	}
}

func TestOverlaps(t *testing.T) {
	th := func(id string, files ...string) Thread {
		c := SourceChanges{Source: "web"}
		for _, f := range files {
			c.Files = append(c.Files, tunnel.GitFileChange{Path: f, Status: "M"})
		}
		return Thread{Session: db.AgentSession{ID: id}, Changes: []SourceChanges{c}}
	}
	got := Overlaps([]Thread{th("a", "x", "y"), th("b", "y"), th("c", "z", "x")})
	if len(got) != 2 || got[0].Path != "x" || len(got[0].Threads) != 2 || got[1].Path != "y" {
		t.Fatalf("overlaps %+v", got)
	}
}
