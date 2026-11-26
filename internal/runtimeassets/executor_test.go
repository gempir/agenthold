package runtimeassets

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func buildTestExecutorArchive(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			if tf == tar.TypeDir {
				mode = 0o755
			} else {
				mode = 0o644
			}
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: tf,
			Mode:     mode,
			Linkname: e.linkname,
		}
		if tf == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := enc.EncodeAll(raw.Bytes(), nil)
	_ = enc.Close()
	return out
}

func TestPrepareExecutorExtractCacheAndLinks(t *testing.T) {
	m, files, _, _ := fixture(t)
	archive := buildTestExecutorArchive(t, []tarEntry{
		{name: "package/executor", body: "#!/bin/sh\necho ok\n", mode: 0o755},
		{name: "package/runtime/git/bin/git", body: "git-bin", mode: 0o755},
		{name: "package/runtime/git/libexec/git-core/git", typeflag: tar.TypeLink, linkname: "package/runtime/git/bin/git"},
		{name: "package/runtime/git/libexec/git-core/git-status", typeflag: tar.TypeSymlink, linkname: "git"},
	})
	files[executorArchiveName] = &fstest.MapFile{Data: archive}
	d := digest(archive)
	m.Executor = &executorArtifact{Version: "2.0.0-beta.79", SHA256: d.SHA256, Size: d.Size}
	setManifest(t, files, m)

	dir := privateDir(t)
	got, err := prepareExecutor(t.Context(), dir, files)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Bundled || got.Version != "2.0.0-beta.79" {
		t.Fatalf("unexpected paths: %+v", got)
	}
	body, err := os.ReadFile(got.Binary)
	if err != nil || string(body) != "#!/bin/sh\necho ok\n" {
		t.Fatalf("binary=%q err=%v", body, err)
	}
	linkTarget, err := os.Readlink(filepath.Join(got.Dir, "runtime/git/libexec/git-core/git-status"))
	if err != nil || linkTarget != "git" {
		t.Fatalf("symlink=%q err=%v", linkTarget, err)
	}
	linked, err := os.ReadFile(filepath.Join(got.Dir, "runtime/git/libexec/git-core/git"))
	if err != nil || string(linked) != "git-bin" {
		t.Fatalf("hardlink=%q err=%v", linked, err)
	}

	// Second call hits the cached extracted directory even if the embedded archive is removed.
	delete(files, executorArchiveName)
	got2, err := prepareExecutor(t.Context(), dir, files)
	if err != nil || got2.Binary != got.Binary {
		t.Fatalf("cached prepare: %+v err=%v", got2, err)
	}
}

func TestPrepareExecutorRejectsTraversalAndCorruption(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		corrupt bool
	}{
		{
			name:    "dotdot path",
			entries: []tarEntry{{name: "package/../escape", body: "bad"}},
		},
		{
			name:    "symlink escape",
			entries: []tarEntry{{name: "package/executor", body: "ok", mode: 0o755}, {name: "package/evil", typeflag: tar.TypeSymlink, linkname: "../../etc/passwd"}},
		},
		{
			name:    "absolute symlink",
			entries: []tarEntry{{name: "package/executor", body: "ok", mode: 0o755}, {name: "package/evil", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		},
		{
			name:    "checksum mismatch",
			entries: []tarEntry{{name: "package/executor", body: "ok", mode: 0o755}},
			corrupt: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, files, _, _ := fixture(t)
			archive := buildTestExecutorArchive(t, tc.entries)
			d := digest(archive)
			if tc.corrupt {
				archive[len(archive)-1] ^= 0xFF
			}
			files[executorArchiveName] = &fstest.MapFile{Data: archive}
			m.Executor = &executorArtifact{Version: "2.0.0-beta.79", SHA256: d.SHA256, Size: d.Size}
			setManifest(t, files, m)

			if _, err := prepareExecutor(t.Context(), privateDir(t), files); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPrepareExecutorUnbundledNoOp(t *testing.T) {
	if bundledFiles() != nil {
		t.Skip("release build")
	}
	dir := filepath.Join(privateDir(t), "missing")
	got, err := PrepareExecutor(t.Context(), dir)
	if err != nil || got.Bundled || got != (ExecutorPaths{}) {
		t.Fatalf("unbundled: %+v err=%v", got, err)
	}
}
