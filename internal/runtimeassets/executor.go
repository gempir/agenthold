package runtimeassets

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	executorArchiveName = "executor.tar.zst"
	executorMarkerFile  = ".executor.sha256"
	// maxExecutorExtracted caps total extracted bytes as a safety guard against zip bombs.
	maxExecutorExtracted = 2 << 30 // 2 GiB
	maxExecutorEntries   = 20000
)

// ExecutorPaths describes the embedded Executor runtime extracted from a release binary.
// Unbundled development builds return Bundled=false.
type ExecutorPaths struct {
	Bundled bool
	Version string
	Dir     string
	Binary  string
}

// PrepareExecutor extracts the bundled Executor runtime (if embedded in this release binary)
// into dataDir/runtime/<releaseVersion>/executor/ and returns the path to its executable.
// Unlike Prepare, it needs neither KVM nor guest assets, so controller-only hosts can run Executor.
func PrepareExecutor(ctx context.Context, dataDir string) (ExecutorPaths, error) {
	assets := bundledFiles()
	if assets == nil {
		return ExecutorPaths{}, nil
	}
	return prepareExecutor(ctx, dataDir, assets)
}

func prepareExecutor(ctx context.Context, dataDir string, assets fs.FS) (ExecutorPaths, error) {
	if err := ctx.Err(); err != nil {
		return ExecutorPaths{}, err
	}
	m, err := readManifest(assets)
	if err != nil {
		return ExecutorPaths{}, err
	}
	if m.Executor == nil {
		return ExecutorPaths{}, nil
	}
	if dataDir == "" {
		return ExecutorPaths{}, errors.New("runtime assets: data directory is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return ExecutorPaths{}, err
	}
	if err := secureDir(abs); err != nil {
		return ExecutorPaths{}, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return ExecutorPaths{}, err
	}
	runtimeDir := filepath.Join(abs, "runtime")
	versionDir := filepath.Join(runtimeDir, m.Version)
	for _, dir := range []string{runtimeDir, versionDir} {
		if err := secureDir(dir); err != nil {
			return ExecutorPaths{}, err
		}
	}

	unlock, err := lockBase(ctx, versionDir, "executor-"+m.Executor.SHA256)
	if err != nil {
		return ExecutorPaths{}, err
	}
	defer unlock()

	execDir := filepath.Join(versionDir, "executor")
	binary := filepath.Join(execDir, "executor")
	if executorReady(execDir, m.Executor.SHA256) {
		return ExecutorPaths{Bundled: true, Version: m.Executor.Version, Dir: execDir, Binary: binary}, nil
	}

	f, err := assets.Open(executorArchiveName)
	if err != nil {
		return ExecutorPaths{}, fmt.Errorf("embedded %s: %w", executorArchiveName, err)
	}
	defer f.Close()

	tmpDir, err := os.MkdirTemp(versionDir, ".agenthold-executor-*")
	if err != nil {
		return ExecutorPaths{}, err
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chmod(tmpDir, 0o755); err != nil {
		return ExecutorPaths{}, err
	}

	if err := extractVerifiedExecutor(ctx, f, artifact{SHA256: m.Executor.SHA256, Size: m.Executor.Size}, tmpDir); err != nil {
		return ExecutorPaths{}, fmt.Errorf("extract executor: %w", err)
	}
	tmpRoot, err := os.OpenRoot(tmpDir)
	if err != nil {
		return ExecutorPaths{}, err
	}
	fi, err := tmpRoot.Lstat("executor")
	if err != nil || !fi.Mode().IsRegular() {
		_ = tmpRoot.Close()
		return ExecutorPaths{}, errors.New("extract executor: archive did not contain executable package/executor")
	}
	if err := tmpRoot.Chmod("executor", 0o755); err != nil {
		_ = tmpRoot.Close()
		return ExecutorPaths{}, err
	}
	binSHA, err := fileSHA256InRoot(tmpRoot, "executor")
	if err != nil {
		_ = tmpRoot.Close()
		return ExecutorPaths{}, err
	}
	markerBody := fmt.Sprintf("archive:%s\nbinary:%s\n", m.Executor.SHA256, binSHA)
	if err := tmpRoot.WriteFile(executorMarkerFile, []byte(markerBody), 0o644); err != nil {
		_ = tmpRoot.Close()
		return ExecutorPaths{}, err
	}
	_ = tmpRoot.Close()

	if err := removeIfExistsNoFollow(execDir); err != nil {
		return ExecutorPaths{}, err
	}
	if err := os.Rename(tmpDir, execDir); err != nil {
		return ExecutorPaths{}, err
	}
	return ExecutorPaths{Bundled: true, Version: m.Executor.Version, Dir: execDir, Binary: binary}, nil
}

func removeIfExistsNoFollow(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return os.Remove(p)
	}
	return os.RemoveAll(p)
}

func executorReady(execDir, wantArchiveSHA string) bool {
	dirInfo, err := os.Lstat(execDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(dirInfo) {
		return false
	}
	root, err := os.OpenRoot(execDir)
	if err != nil {
		return false
	}
	defer root.Close()

	markerInfo, err := root.Lstat(executorMarkerFile)
	if err != nil || !markerInfo.Mode().IsRegular() || !ownedByCurrentUser(markerInfo) {
		return false
	}
	marker, err := root.ReadFile(executorMarkerFile)
	if err != nil {
		return false
	}
	var gotArchive, gotBinary string
	for line := range strings.SplitSeq(strings.TrimSpace(string(marker)), "\n") {
		if v, ok := strings.CutPrefix(line, "archive:"); ok {
			gotArchive = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "binary:"); ok {
			gotBinary = strings.TrimSpace(v)
		}
	}
	if gotArchive != wantArchiveSHA || len(gotBinary) != 64 {
		return false
	}
	binInfo, err := root.Lstat("executor")
	if err != nil || !binInfo.Mode().IsRegular() || binInfo.Mode().Perm()&0o100 == 0 || !ownedByCurrentUser(binInfo) {
		return false
	}
	actualSHA, err := fileSHA256InRoot(root, "executor")
	return err == nil && actualSHA == gotBinary
}

func fileSHA256InRoot(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractVerifiedExecutor(ctx context.Context, r io.Reader, want artifact, destDir string) error {
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return err
	}
	defer root.Close()

	h := sha256.New()
	counted := &countingReader{r: io.TeeReader(io.LimitReader(contextReader{ctx, r}, want.Size+1), h)}
	dec, err := zstd.NewReader(counted, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return err
	}
	defer dec.Close()

	tr := tar.NewReader(dec)
	var totalBytes int64
	entries := 0
	type hardlink struct {
		dst    string
		target string
	}
	var pendingLinks []hardlink

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maxExecutorEntries {
			return errors.New("archive has too many entries")
		}
		rel, ok := stripPackagePrefix(hdr.Name)
		if !ok {
			continue
		}
		relPath, err := safeRelPath(rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAllConfined(root, relPath, permOrDefault(hdr.FileInfo().Mode().Perm(), 0o755)); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || totalBytes+hdr.Size > maxExecutorExtracted {
				return errors.New("archive exceeds extracted size limit")
			}
			if parent := path.Dir(relPath); parent != "." {
				if err := mkdirAllConfined(root, parent, 0o755); err != nil {
					return err
				}
			}
			mode := permOrDefault(hdr.FileInfo().Mode().Perm(), 0o644)
			out, err := root.OpenFile(relPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, hdr.Size+1))
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != hdr.Size {
				return errors.New("truncated file in archive")
			}
			totalBytes += n
		case tar.TypeSymlink:
			if err := validateSymlinkTarget(destDir, filepath.Join(destDir, filepath.FromSlash(relPath)), hdr.Linkname); err != nil {
				return err
			}
			if parent := path.Dir(relPath); parent != "." {
				if err := mkdirAllConfined(root, parent, 0o755); err != nil {
					return err
				}
			}
			_ = root.Remove(relPath)
			if err := root.Symlink(hdr.Linkname, relPath); err != nil {
				return err
			}
		case tar.TypeLink:
			linkRel, ok := stripPackagePrefix(hdr.Linkname)
			if !ok {
				return fmt.Errorf("invalid hardlink target %q", hdr.Linkname)
			}
			targetRel, err := safeRelPath(linkRel)
			if err != nil {
				return err
			}
			pendingLinks = append(pendingLinks, hardlink{dst: relPath, target: targetRel})
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			// Metadata headers handled by archive/tar.
		default:
			return fmt.Errorf("unsupported tar entry type %q for %q", hdr.Typeflag, hdr.Name)
		}
	}
	// Drain any trailing zero blocks / zstd frame epilogue so sha256 covers the whole archive.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return err
	}
	if counted.n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return errChecksum
	}
	for _, l := range pendingLinks {
		if parent := path.Dir(l.dst); parent != "." {
			if err := mkdirAllConfined(root, parent, 0o755); err != nil {
				return err
			}
		}
		targetInfo, err := root.Lstat(l.target)
		if err != nil || !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("hardlink target %q must be a regular file", l.target)
		}
		_ = root.Remove(l.dst)
		if err := root.Link(l.target, l.dst); err != nil {
			return err
		}
	}
	return nil
}

// mkdirAllConfined creates every slash-separated directory component inside root,
// rejecting any existing non-directory or symlink component along the way.
func mkdirAllConfined(root *os.Root, rel string, mode os.FileMode) error {
	parts := strings.Split(rel, "/")
	cur := ""
	for i, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		fi, err := root.Lstat(cur)
		if err == nil {
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("archive path component %q is not a directory", cur)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		perm := os.FileMode(0o755)
		if i == len(parts)-1 && mode != 0 {
			perm = mode
		}
		if err := root.Mkdir(cur, perm); err != nil {
			return err
		}
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// stripPackagePrefix removes the leading "package/" directory from npm/executor tarballs.
func stripPackagePrefix(name string) (string, bool) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || clean == "package" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(clean, "package/"); ok {
		clean = rest
	}
	if clean == "" || clean == "." {
		return "", false
	}
	return clean, true
}

func safeRelPath(rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\x00") || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("unsafe archive path %q", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("unsafe archive path %q", rel)
	}
	return clean, nil
}

func validateSymlinkTarget(root, linkPath, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\x00") || strings.Contains(target, "\\") {
		return fmt.Errorf("unsafe symlink target %q", target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(target)))
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink %q -> %q escapes root", linkPath, target)
	}
	return nil
}

func permOrDefault(p, def os.FileMode) os.FileMode {
	if p == 0 {
		return def
	}
	return p & 0o777
}
