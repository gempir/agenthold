package sandboxd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteText(t *testing.T) {
	s, home := setup(t)
	write(t, filepath.Join(home, "src", "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(home, "img.png"), "\x89PNG\r\n\x1a\n\x00\x00\x00")
	write(t, filepath.Join(home, "lines.txt"), "1\n2\n3\n4\n5\n")
	write(t, filepath.Join(home, "big.txt"), strings.Repeat("é", 1000)) // 2000 bytes

	f, err := s.ReadText(t.Context(), "src/main.go", 0, 0, 0)
	if err != nil || f.Path != "src/main.go" || f.IsBinary || f.Truncated || f.Content != "package main\n\nfunc main() {}\n" || f.Size != 29 || f.ModTime == 0 || f.MimeType == "" {
		t.Fatalf("read %+v %v", f, err)
	}
	if f, err = s.ReadText(t.Context(), "img.png", 0, 0, 0); err != nil || !f.IsBinary || f.Content != "" || f.MimeType != "image/png" {
		t.Fatalf("binary %+v %v", f, err)
	}
	if f, err = s.ReadText(t.Context(), "big.txt", 1001, 0, 0); err != nil || !f.Truncated || len(f.Content) != 1000 || f.IsBinary {
		t.Fatalf("truncated (cut at a character) %d %+v %v", len(f.Content), f.Truncated, err)
	}
	if f, err = s.ReadText(t.Context(), "lines.txt", 0, 2, 3); err != nil || f.Content != "2\n3\n" || f.StartLine != 2 || f.EndLine != 3 || f.TotalLines != 5 {
		t.Fatalf("line range %+v %v", f, err)
	}
	if f, err = s.ReadText(t.Context(), "lines.txt", 0, 4, 0); err != nil || f.Content != "4\n5\n" || f.EndLine != 5 {
		t.Fatalf("open line range %+v %v", f, err)
	}

	// Paths stay inside the home, also through symlinks.
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "x")
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/hosts", "../x", "escape/secret", "", "src"} {
		if _, err := s.ReadText(t.Context(), p, 0, 0, 0); err == nil {
			t.Fatalf("read %q: no error", p)
		}
	}
	if _, err := s.ReadText(t.Context(), "src/main.go", 0, 0, 0, filepath.Join(home, "src")); err == nil {
		t.Fatal("denied path read")
	}

	// Writes are atomic, create parents and keep an existing file's mode.
	if err := os.Chmod(filepath.Join(home, "lines.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := s.WriteText(t.Context(), "lines.txt", []byte("#!/bin/sh\necho hi\n"))
	if err != nil || w.Size != 18 || w.Path != "lines.txt" {
		t.Fatalf("write %+v %v", w, err)
	}
	fi, _ := os.Stat(filepath.Join(home, "lines.txt"))
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := s.WriteText(t.Context(), "new/dir/file.md", []byte("# hi\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "new/dir/file.md")); string(b) != "# hi\n" {
		t.Fatalf("written %q", b)
	}
	// A symlink inside the home is kept; its target is written.
	if err := os.Symlink("src/main.go", filepath.Join(home, "link.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteText(t.Context(), "link.go", []byte("package x\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(home, "link.go")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(home, "src/main.go")); string(b) != "package x\n" {
		t.Fatalf("target %q", b)
	}
	for _, p := range []string{"/tmp/x", "../x", "escape/secret", "escape/new", "src"} {
		if _, err := s.WriteText(t.Context(), p, []byte("x")); err == nil {
			t.Fatalf("write %q: no error", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "x" {
		t.Fatal("wrote outside the home")
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agenthold-edit-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestSearchFiles(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	for _, p := range []string{
		"proj/src/components/Button.tsx", "proj/src/components/ButtonGroup.tsx", "proj/src/util/format.ts",
		"proj/README.md", "proj/node_modules/lib/index.js", "proj/dist/bundle.js", "proj/ignored/x.ts",
		"other/notes.md", "other/node_modules/dep/index.js", "other/.venv/lib/site.py",
	} {
		write(t, filepath.Join(home, p), "x\n")
	}
	write(t, filepath.Join(home, "proj/.gitignore"), "node_modules/\ndist/\nignored/\n")
	gitT(t, filepath.Join(home, "proj"), "init", "-q", "-b", "main")

	r, err := s.SearchFiles(t.Context(), "proj", "button", 10)
	if err != nil || r.Base != "proj" || len(r.Results) < 2 || r.Results[0].Path != "src/components/Button.tsx" || r.Results[1].Path != "src/components/ButtonGroup.tsx" {
		t.Fatalf("search %+v %v", r, err)
	}
	r, err = s.SearchFiles(t.Context(), "proj", "scfmt", 10) // a subsequence
	if err != nil || len(r.Results) == 0 || r.Results[0].Path != "src/util/format.ts" {
		t.Fatalf("fuzzy %+v %v", r, err)
	}
	r, err = s.SearchFiles(t.Context(), "proj", "index", 10)
	if err != nil || len(r.Results) != 0 {
		t.Fatalf("gitignored files found %+v %v", r, err)
	}
	r, err = s.SearchFiles(t.Context(), "proj", "components", 10)
	if err != nil || len(r.Results) == 0 || r.Results[0].Path != "src/components" || !r.Results[0].IsDir {
		t.Fatalf("directory %+v %v", r, err)
	}
	r, err = s.SearchFiles(t.Context(), "proj", "", 2)
	if err != nil || len(r.Results) != 2 || !r.Truncated {
		t.Fatalf("empty query %+v %v", r, err)
	}

	// Outside a repository: a walk that skips dependency directories.
	r, err = s.SearchFiles(t.Context(), "other", "index", 10)
	if err != nil || len(r.Results) != 0 {
		t.Fatalf("node_modules searched %+v %v", r, err)
	}
	r, err = s.SearchFiles(t.Context(), "", "notes", 10)
	if err != nil || r.Base != "" || len(r.Results) != 1 || r.Results[0].Path != "other/notes.md" {
		t.Fatalf("home search %+v %v", r, err)
	}
	if _, err := s.SearchFiles(t.Context(), "/etc", "hosts", 10); err == nil {
		t.Fatal("searched outside the home")
	}
}
