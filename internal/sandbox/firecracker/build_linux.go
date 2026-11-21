//go:build linux

package firecracker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// BuildLayerOptions configures an ephemeral Firecracker microVM used to build
// a custom image delta layer on top of the node's base image.
type BuildLayerOptions struct {
	BuildID        string
	BootFiles      map[string][]byte
	Env            map[string]string
	CPUs           float64
	MemoryMB       int64
	DiskGB         int64
	TunnelPort     int
	SetToken       func(ctx context.Context, agentID, hash string) error
	WaitSession    func(ctx context.Context, agentID string) (*tunnel.Session, error)
	RegisterEgress func(agentID string, subnet netip.Prefix)
	RemoveEgress   func(agentID string)
	OnLog          func(line string)
}

// BuildLayerOutput holds the sealed, shrunk, and zstd-compressed ext4 layer
// produced by Driver.BuildLayer. Call Cleanup once the files are no longer needed.
type BuildLayerOutput struct {
	Arch       string
	BaseKey    string
	RawPath    string
	RawSHA256  string
	RawSize    int64
	ZstdPath   string
	ZstdSHA256 string
	ZstdSize   int64
	Cleanup    func()
}

// StopCold shuts down a microVM cleanly without writing a memory snapshot.
func (d *Driver) StopCold(ctx context.Context, id sandbox.ID) error {
	unlock, err := d.lockIdle(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := d.load(id)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := d.discardSnapshot(id); err != nil {
		return err
	}
	return d.stop(ctx, id, m)
}

// BuildLayer boots an ephemeral jailed microVM on top of BaseImagePath, runs
// /etc/agenthold/build-run.sh as root while streaming logs to opts.OnLog,
// cleanly powers off the VM, shrinks and strips the journal from its overlay
// ext4, and compresses it with zstd.
func (d *Driver) BuildLayer(ctx context.Context, opts BuildLayerOptions) (BuildLayerOutput, error) {
	if !validID(opts.BuildID) {
		return BuildLayerOutput{}, fmt.Errorf("invalid build id %q", opts.BuildID)
	}
	if opts.SetToken == nil || opts.WaitSession == nil {
		return BuildLayerOutput{}, errors.New("build layer requires tunnel callbacks")
	}
	cpus := opts.CPUs
	if cpus < 1 {
		cpus = 2
	}
	mem := opts.MemoryMB
	if mem < 1024 {
		mem = 2048
	}
	diskGB := opts.DiskGB
	if diskGB < 2 {
		diskGB = 8
	}

	buildAgentID := "bld_" + opts.BuildID
	if len(buildAgentID) > 120 {
		buildAgentID = buildAgentID[:120]
	}
	spec := sandbox.Spec{
		AgentID:   buildAgentID,
		Resources: sandbox.Resources{CPUs: cpus, MemoryMB: mem, DiskGB: diskGB},
		Env:       opts.Env,
	}
	id, err := d.Create(ctx, spec)
	if err != nil {
		return BuildLayerOutput{}, fmt.Errorf("create builder sandbox: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Destroy(c, id, sandbox.DestroyOpts{})
	}()

	st, err := d.Status(ctx, id)
	if err != nil {
		return BuildLayerOutput{}, fmt.Errorf("builder sandbox status: %w", err)
	}
	if opts.RegisterEgress != nil && st.Subnet.IsValid() {
		opts.RegisterEgress(buildAgentID, st.Subnet)
		defer func() {
			if opts.RemoveEgress != nil {
				opts.RemoveEgress(buildAgentID)
			}
		}()
	}

	var rawTok [32]byte
	if _, err := rand.Read(rawTok[:]); err != nil {
		return BuildLayerOutput{}, err
	}
	token := hex.EncodeToString(rawTok[:])
	sum := sha256.Sum256([]byte(token))
	if err := opts.SetToken(ctx, buildAgentID, hex.EncodeToString(sum[:])); err != nil {
		return BuildLayerOutput{}, fmt.Errorf("set builder boot token: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = opts.SetToken(c, buildAgentID, "")
	}()

	logLine := func(line string) {
		if opts.OnLog != nil {
			opts.OnLog(line)
		}
	}
	logLine("==> Booting isolated builder microVM")
	boot := sandbox.Boot{
		AgentID:    buildAgentID,
		Token:      token,
		TunnelPort: opts.TunnelPort,
		Files:      opts.BootFiles,
	}
	if err := d.Start(ctx, id, boot); err != nil {
		return BuildLayerOutput{}, fmt.Errorf("start builder VM: %w", err)
	}
	sess, err := opts.WaitSession(ctx, buildAgentID)
	if err != nil {
		return BuildLayerOutput{}, fmt.Errorf("wait for builder VM tunnel: %w", err)
	}

	if err := runBuildScript(ctx, sess, logLine); err != nil {
		return BuildLayerOutput{}, err
	}

	logLine("==> Shutting down builder microVM")
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stopCancel()
	if err := d.StopCold(stopCtx, id); err != nil {
		return BuildLayerOutput{}, fmt.Errorf("stop builder VM: %w", err)
	}

	logLine("==> Sealing and compressing image layer")
	tmpDir, err := os.MkdirTemp(d.cfg.DataDir, ".imgbuild-*")
	if err != nil {
		return BuildLayerOutput{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	rawPath := filepath.Join(tmpDir, "layer.ext4")
	if err := copyFileSparse(filepath.Join(d.dir(id), "overlay.ext4"), rawPath); err != nil {
		return BuildLayerOutput{}, fmt.Errorf("copy overlay disk: %w", err)
	}
	if err := sealExt4Layer(ctx, rawPath); err != nil {
		return BuildLayerOutput{}, err
	}
	rawSHA, rawSize, err := hashFile(rawPath)
	if err != nil {
		return BuildLayerOutput{}, err
	}
	zstdPath := filepath.Join(tmpDir, "layer.ext4.zst")
	zstdSHA, zstdSize, err := compressFileZstd(rawPath, zstdPath)
	if err != nil {
		return BuildLayerOutput{}, err
	}
	if err := os.Chmod(rawPath, 0o644); err != nil {
		return BuildLayerOutput{}, err
	}
	if err := os.Chmod(zstdPath, 0o644); err != nil {
		return BuildLayerOutput{}, err
	}
	logLine(fmt.Sprintf("==> Layer ready (%s, %.1f MiB raw, %.1f MiB compressed)", runtime.GOARCH, float64(rawSize)/(1<<20), float64(zstdSize)/(1<<20)))
	ok = true
	return BuildLayerOutput{
		Arch:       runtime.GOARCH,
		BaseKey:    strings.TrimSuffix(filepath.Base(d.cfg.BaseImagePath), ".ext4"),
		RawPath:    rawPath,
		RawSHA256:  rawSHA,
		RawSize:    rawSize,
		ZstdPath:   zstdPath,
		ZstdSHA256: zstdSHA,
		ZstdSize:   zstdSize,
		Cleanup:    func() { _ = os.RemoveAll(tmpDir) },
	}, nil
}

func runBuildScript(ctx context.Context, sess *tunnel.Session, onLog func(string)) error {
	var mu sync.Mutex
	emit := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		onLog(line)
	}
	p, err := sess.StartHarness(ctx, []string{"sudo", "-E", "bash", "/etc/agenthold/build-run.sh"}, nil, emit)
	if err != nil {
		return fmt.Errorf("start build script: %w", err)
	}
	defer p.Close()
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()

	sc := bufio.NewScanner(p.Stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		emit(sc.Text())
	}
	<-p.Done()
	code, err := p.ExitCode()
	if err != nil {
		return fmt.Errorf("build script exited abnormally: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("build script exited with code %d", code)
	}
	return nil
}

// sealExt4Layer checks, strips the journal (the layer is mounted read-only),
// shrinks to minimum size, and truncates path to its exact block size.
func sealExt4Layer(ctx context.Context, path string) error {
	runE2fsck := func() error {
		cmd := exec.CommandContext(ctx, "e2fsck", "-fy", path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			var ee *exec.ExitError
			// e2fsck exit code 1 means filesystem errors were corrected.
			if errors.As(err, &ee) && ee.ExitCode() <= 1 {
				return nil
			}
			return fmt.Errorf("e2fsck: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := runE2fsck(); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tune2fs", "-O", "^has_journal", path).CombinedOutput(); err != nil {
		return fmt.Errorf("tune2fs ^has_journal: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "resize2fs", "-M", path).CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs -M: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := runE2fsck(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "dumpe2fs", "-h", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dumpe2fs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var blocks, bsize int64
	for line := range strings.SplitSeq(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "Block count":
				blocks, _ = strconv.ParseInt(v, 10, 64)
			case "Block size":
				bsize, _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	if blocks <= 0 || bsize <= 0 {
		return fmt.Errorf("dumpe2fs returned invalid geometry (blocks=%d bsize=%d)", blocks, bsize)
	}
	return os.Truncate(path, blocks*bsize)
}

func copyFileSparse(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func compressFileZstd(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	cw := &countingWriter{w: io.MultiWriter(out, h)}
	enc, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		_ = out.Close()
		return "", 0, err
	}
	_, copyErr := io.Copy(enc, in)
	encErr := enc.Close()
	closeErr := out.Close()
	if err := errors.Join(copyErr, encErr, closeErr); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
