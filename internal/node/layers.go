package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// MaxLayerCompressedBytes caps a single custom image layer upload (4 GiB).
const MaxLayerCompressedBytes = 4 << 30

// MaxLayerRawBytes caps a single uncompressed custom image layer (16 GiB).
const MaxLayerRawBytes = 16 << 30

// LayerArtifact describes a content-addressed read-only ext4 layer.
type LayerArtifact struct {
	// Ref is the logical layer reference stored in sandbox.Spec.Layers
	// (for example "image:img_abc:2").
	Ref        string `json:"ref,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	ZstdSHA256 string `json:"zstdSha256"`
	ZstdSize   int64  `json:"zstdSize"`
	Subdir     string `json:"subdir,omitempty"`
}

// LayerBuildRequest describes a custom image layer build to execute inside an
// isolated builder microVM on a node.
type LayerBuildRequest struct {
	BuildID     string            `json:"buildId"`
	ImageID     string            `json:"imageId"`
	Revision    int64             `json:"revision"`
	AptPackages []string          `json:"aptPackages,omitempty"`
	SetupScript string            `json:"setupScript,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	// EgressConfig, when non-nil, injects build-time secrets on the wire via
	// the node's egress proxy without placing real secrets inside the VM.
	EgressConfig *AgentConfig `json:"egressConfig,omitempty"`
	// BuildEnv is injected into the builder VM environment (for example secret
	// placeholders), not persisted into the resulting image's image.env.d.
	BuildEnv map[string]string `json:"buildEnv,omitempty"`
	CPUs     float64           `json:"cpus,omitempty"`
	MemoryMB int64             `json:"memoryMb,omitempty"`
	DiskGB   int64             `json:"diskGb,omitempty"`
}

// LayerBuildResult summarizes the built ext4 layer produced by BuildLayer.
type LayerBuildResult struct {
	Arch     string        `json:"arch"`
	BaseKey  string        `json:"baseKey,omitempty"`
	Artifact LayerArtifact `json:"artifact"`
}

// LayerHost is implemented by nodes (Local and Remote) that store and build
// custom rootfs layers.
type LayerHost interface {
	HasLayer(ctx context.Context, ref, sha256 string) (bool, error)
	PutLayer(ctx context.Context, meta LayerArtifact, zstdStream io.Reader) error
	BuildLayer(ctx context.Context, req LayerBuildRequest, onLog func(line string)) (LayerBuildResult, io.ReadCloser, error)
}

// LayerBuilder executes a LayerBuildRequest on a local node and returns the
// built artifact metadata plus a reader over its zstd-compressed ext4 bytes.
type LayerBuilder func(ctx context.Context, req LayerBuildRequest, onLog func(line string)) (LayerBuildResult, io.ReadCloser, error)

var (
	sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	layerRefRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// ValidateLayerSHA checks that s is a 64-char lowercase hex sha256 digest.
func ValidateLayerSHA(s string) error {
	if !sha256HexRe.MatchString(s) {
		return fmt.Errorf("invalid sha256 %q", s)
	}
	return nil
}

// ValidateLayerRef checks that ref is a safe logical layer reference.
func ValidateLayerRef(ref string) error {
	if !layerRefRe.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("invalid layer reference %q", ref)
	}
	return nil
}

// LayerStore manages content-addressed custom image layers under <dataDir>/images/.
type LayerStore struct {
	Dir string
}

// NewLayerStore returns a LayerStore rooted at <dataDir>/images.
func NewLayerStore(dataDir string) *LayerStore {
	return &LayerStore{Dir: filepath.Join(dataDir, "images")}
}

// RawPath returns the path to an uncompressed read-only ext4 layer file.
func (s *LayerStore) RawPath(sha string) string { return filepath.Join(s.Dir, sha+".ext4") }

// ZstdPath returns the path to a zstd-compressed layer archive.
func (s *LayerStore) ZstdPath(sha string) string { return filepath.Join(s.Dir, sha+".ext4.zst") }

func (s *LayerStore) refPath(ref string) string {
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(ref)
	return filepath.Join(s.Dir, "refs", safe+".json")
}

type layerRefRecord struct {
	SHA256 string `json:"sha256"`
	Subdir string `json:"subdir,omitempty"`
}

func (s *LayerStore) ensureDirs() error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.Dir, "refs"), 0o755)
}

// HasLayer reports whether the node has the raw ext4 file for sha and, when
// ref is non-empty, a ref mapping pointing to sha.
func (s *LayerStore) HasLayer(ref, sha string) (bool, error) {
	if err := ValidateLayerSHA(sha); err != nil {
		return false, err
	}
	st, err := os.Stat(s.RawPath(sha))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return false, nil
	}
	if ref == "" {
		return true, nil
	}
	if err := ValidateLayerRef(ref); err != nil {
		return false, err
	}
	b, err := os.ReadFile(s.refPath(ref))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rec layerRefRecord
	if json.Unmarshal(b, &rec) != nil {
		return false, nil
	}
	return rec.SHA256 == sha, nil
}

// WriteRef binds a logical ref (such as "image:img_1:2") to a cached sha256+subdir.
func (s *LayerStore) WriteRef(ref, sha, subdir string) error {
	if ref == "" {
		return nil
	}
	if err := ValidateLayerRef(ref); err != nil {
		return err
	}
	if err := ValidateLayerSHA(sha); err != nil {
		return err
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	data, err := json.Marshal(layerRefRecord{SHA256: sha, Subdir: subdir})
	if err != nil {
		return err
	}
	return writeAtomicFile(s.refPath(ref), data, 0o644)
}

// ResolveRef resolves a logical layer ref to a verified host ext4 path and subdir.
func (s *LayerStore) ResolveRef(ref string) (path, subdir string, err error) {
	if err := ValidateLayerRef(ref); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(s.refPath(ref))
	if err != nil {
		return "", "", fmt.Errorf("layer %q is not cached on this node: %w", ref, err)
	}
	var rec layerRefRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return "", "", fmt.Errorf("corrupt layer ref %q: %w", ref, err)
	}
	if err := ValidateLayerSHA(rec.SHA256); err != nil {
		return "", "", err
	}
	p := s.RawPath(rec.SHA256)
	if _, err := os.Stat(p); err != nil {
		return "", "", fmt.Errorf("layer file %s missing: %w", rec.SHA256, err)
	}
	return p, rec.Subdir, nil
}

// SaveZstd verifies and stores a zstd-compressed layer archive on the controller.
func (s *LayerStore) SaveZstd(meta LayerArtifact, r io.Reader) error {
	if err := validateLayerArtifact(meta); err != nil {
		return err
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	dst := s.ZstdPath(meta.SHA256)
	if st, err := os.Stat(dst); err == nil && st.Mode().IsRegular() && st.Size() == meta.ZstdSize {
		// Drain reader so streaming callers complete cleanly.
		_, _ = io.Copy(io.Discard, io.LimitReader(r, meta.ZstdSize+1))
		return nil
	}
	tmp, err := os.CreateTemp(s.Dir, ".zst-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, meta.ZstdSize+1))
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != meta.ZstdSize {
		return fmt.Errorf("compressed layer size %d, want %d", n, meta.ZstdSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != meta.ZstdSHA256 {
		return fmt.Errorf("compressed layer sha256 %s, want %s", got, meta.ZstdSHA256)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// OpenZstd opens a stored zstd-compressed layer archive for streaming to a node.
func (s *LayerStore) OpenZstd(sha string) (*os.File, error) {
	if err := ValidateLayerSHA(sha); err != nil {
		return nil, err
	}
	return os.Open(s.ZstdPath(sha))
}

// DeleteZstd removes a stored zstd archive when no builds reference it.
func (s *LayerStore) DeleteZstd(sha string) error {
	if err := ValidateLayerSHA(sha); err != nil {
		return err
	}
	err := os.Remove(s.ZstdPath(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// InstallFromZstd verifies a zstd-compressed layer stream, decompresses it to
// <dataDir>/images/<sha256>.ext4, verifies the uncompressed digest, and binds
// meta.Ref if present.
func (s *LayerStore) InstallFromZstd(meta LayerArtifact, r io.Reader) error {
	if err := validateLayerArtifact(meta); err != nil {
		return err
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	rawDst := s.RawPath(meta.SHA256)
	if st, err := os.Stat(rawDst); err == nil && st.Mode().IsRegular() && st.Size() == meta.Size {
		_, _ = io.Copy(io.Discard, io.LimitReader(r, meta.ZstdSize+1))
		return s.WriteRef(meta.Ref, meta.SHA256, meta.Subdir)
	}
	zHash := sha256.New()
	cr := &countingReader{r: io.TeeReader(io.LimitReader(r, meta.ZstdSize+1), zHash)}
	dec, err := zstd.NewReader(cr)
	if err != nil {
		return fmt.Errorf("zstd decoder: %w", err)
	}
	defer dec.Close()

	tmp, err := os.CreateTemp(s.Dir, ".raw-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	rawHash := sha256.New()
	rawN, err := io.Copy(io.MultiWriter(tmp, rawHash), io.LimitReader(dec, meta.Size+1))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err != nil {
		return fmt.Errorf("decompress layer: %w", err)
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Drain any trailing bytes (up to 1) so compressed size/hash are exact.
	_, _ = io.Copy(io.Discard, cr)
	if cr.n != meta.ZstdSize {
		return fmt.Errorf("compressed layer size %d, want %d", cr.n, meta.ZstdSize)
	}
	if got := hex.EncodeToString(zHash.Sum(nil)); got != meta.ZstdSHA256 {
		return fmt.Errorf("compressed layer sha256 %s, want %s", got, meta.ZstdSHA256)
	}
	if rawN != meta.Size {
		return fmt.Errorf("layer size %d, want %d", rawN, meta.Size)
	}
	if got := hex.EncodeToString(rawHash.Sum(nil)); got != meta.SHA256 {
		return fmt.Errorf("layer sha256 %s, want %s", got, meta.SHA256)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, rawDst); err != nil {
		return err
	}
	return s.WriteRef(meta.Ref, meta.SHA256, meta.Subdir)
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

func validateLayerArtifact(meta LayerArtifact) error {
	if meta.Ref != "" {
		if err := ValidateLayerRef(meta.Ref); err != nil {
			return err
		}
	}
	if err := ValidateLayerSHA(meta.SHA256); err != nil {
		return fmt.Errorf("layer %w", err)
	}
	if err := ValidateLayerSHA(meta.ZstdSHA256); err != nil {
		return fmt.Errorf("compressed layer %w", err)
	}
	if meta.Size <= 0 || meta.Size > MaxLayerRawBytes {
		return fmt.Errorf("invalid layer size %d", meta.Size)
	}
	if meta.ZstdSize <= 0 || meta.ZstdSize > MaxLayerCompressedBytes {
		return fmt.Errorf("invalid compressed layer size %d", meta.ZstdSize)
	}
	return nil
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// BuildScripts generates the builder VM runner script, user setup script, and
// /etc/agenthold/image.env.d/10-custom.env drop-in for a LayerBuildRequest.
func BuildScripts(aptPackages []string, setupScript string, env map[string]string) (runSh, setupSh, envDropIn string) {
	var run strings.Builder
	run.WriteString("#!/bin/bash\nset -euo pipefail\n")
	run.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	// Guide popular toolchain installers into /opt and /usr/local so they are
	// not shadowed by each agent's separate /home/agent disk at runtime.
	run.WriteString("export CARGO_HOME=/opt/cargo RUSTUP_HOME=/opt/rustup GOPATH=/opt/go\n")
	run.WriteString("export PIPX_HOME=/opt/pipx PIPX_BIN_DIR=/usr/local/bin\n")
	run.WriteString("export UV_TOOL_DIR=/opt/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin\n")
	run.WriteString("export PATH=/opt/go/bin:/opt/cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n\n")

	if len(aptPackages) > 0 {
		run.WriteString("echo '==> Installing APT packages'\n")
		run.WriteString("apt-get update -q\n")
		run.WriteString("apt-get install -y -q --no-install-recommends")
		for _, pkg := range aptPackages {
			run.WriteString(" ")
			run.WriteString(shellQuote(pkg))
		}
		run.WriteString("\n\n")
	}

	if strings.TrimSpace(setupScript) != "" {
		setupSh = "#!/bin/bash\nset -euo pipefail\n" + setupScript + "\n"
		run.WriteString("echo '==> Running setup script'\n")
		run.WriteString("bash /etc/agenthold/build-setup.sh\n\n")
	}

	// Warn if anything was written into /home/agent, which is shadowed by the
	// persistent home disk when agents boot.
	run.WriteString("if [ -d /home/agent ] && [ -n \"$(ls -A /home/agent 2>/dev/null)\" ]; then\n")
	run.WriteString("  echo 'WARNING: files were written under /home/agent during build; at runtime /home/agent is a separate persistent disk and will hide them. Install tools under /opt or /usr/local, and place default dotfiles in /etc/skel.'\n")
	run.WriteString("fi\n\n")

	// Clean temporary/package caches and strip per-boot files from the upper
	// layer so runtime boot owns them cleanly.
	run.WriteString("echo '==> Cleaning image layer'\n")
	run.WriteString("apt-get clean || true\n")
	run.WriteString("rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/cache/debconf/*-old /var/lib/dpkg/*-old \\\n")
	run.WriteString("  /root/.npm /root/.cache /tmp/* /var/tmp/* /var/log/*.log /var/log/apt/* \\\n")
	run.WriteString("  /etc/agenthold/build-run.sh /etc/agenthold/build-setup.sh\n")
	run.WriteString("for f in etc/hostname etc/hosts etc/machine-id etc/resolv.conf etc/agenthold/ca.pem etc/agenthold/ca-bundle.pem etc/agenthold/instance-id etc/profile.d/agenthold-path.sh; do\n")
	run.WriteString("  rm -f \"/run/agenthold/disk/upper/$f\" 2>/dev/null || true\n")
	run.WriteString("done\n")
	run.WriteString("sync\n")
	run.WriteString("echo '==> Build finished'\n")

	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var eb bytes.Buffer
		eb.WriteString("# Custom image environment (generated by agenthold)\n")
		for _, k := range keys {
			fmt.Fprintf(&eb, "%s=%s\n", k, env[k])
		}
		envDropIn = eb.String()
	}
	return run.String(), setupSh, envDropIn
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
