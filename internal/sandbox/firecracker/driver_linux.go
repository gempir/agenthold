//go:build linux

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/sandbox"
)

// Driver serializes operations using a context-cancellable filesystem lock.
// The lock also protects address allocation across reopened Driver instances.
// VMMs survive controller restarts; persisted PID start times prevent signaling
// unrelated processes after PID reuse. No boot token is stored in a manifest.
type Driver struct {
	cfg Config
	// disk is an internal provisioning seam for unprivileged unit tests.
	disk func(string, int64, uint32, uint32) error
}

var (
	_ sandbox.Driver   = (*Driver)(nil)
	_ sandbox.Attacher = (*Driver)(nil)
)

type process struct {
	PID       int    `json:"pid"`
	StartTime string `json:"startTime"`
}
type manifest struct {
	BaseImagePath string        `json:"baseImagePath"`
	Version       int           `json:"version"`
	Spec          sandbox.Spec  `json:"spec"`
	Subnet        netip.Prefix  `json:"subnet"`
	UID           uint32        `json:"uid"`
	GID           uint32        `json:"gid"`
	State         sandbox.State `json:"state"`
	Process       process       `json:"process,omitempty"`
}

// New validates runtime artifacts and opens the persistent driver. It does not
// stop existing microVMs. Call Attach for running VMs during reconciliation to
// restore node-side network attribution/policy.
func New(ctx context.Context, cfg Config) (*Driver, error) {
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.RegisterNetwork == nil {
		return nil, errors.New("guest firewall register/unregister callbacks are required")
	}
	if os.Geteuid() != 0 {
		return nil, ErrUnsupported
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open /dev/kvm: %w", ErrUnsupported, err)
	}
	_ = f.Close()
	for name, p := range map[string]string{"base image": cfg.BaseImagePath, "kernel": cfg.KernelPath, "firecracker": cfg.FirecrackerPath, "jailer": cfg.JailerPath} {
		st, e := os.Stat(p)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s must be a regular file", name)
		}
		if (name == "jailer" || name == "firecracker") && st.Mode()&0o111 == 0 {
			return nil, fmt.Errorf("%s is not executable", name)
		}
		if (name == "base image" || name == "kernel") && st.Mode()&0o004 == 0 {
			return nil, fmt.Errorf("%s must be readable by jailed VMM users", name)
		}
	}
	for _, dir := range []string{cfg.DataDir, filepath.Join(cfg.DataDir, "agents"), cfg.JailDir} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	d := &Driver{cfg: cfg}
	unlock, err := d.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Fail closed on corrupt/foreign manifests: never reuse a potentially live
	// VM's addresses or uid. New performs no destructive reconciliation.
	if _, err = d.manifests(); err != nil {
		return nil, err
	}
	return d, nil
}

// Name implements sandbox.Driver.
func (d *Driver) Name() string             { return "firecracker" }
func (d *Driver) dir(id sandbox.ID) string { return filepath.Join(d.cfg.DataDir, "agents", string(id)) }
func (d *Driver) jailID(id sandbox.ID) string {
	s := sha256.Sum256([]byte(id))
	return "ah" + hex.EncodeToString(s[:12])
}

func (d *Driver) jailRoot(id sandbox.ID) string {
	return filepath.Join(d.cfg.JailDir, filepath.Base(d.cfg.FirecrackerPath), d.jailID(id), "root")
}
func (d *Driver) socket(id sandbox.ID) string { return filepath.Join(d.jailRoot(id), "api.sock") }

// TapName is stable across boots and controller restarts, and always matches
// the host firewall's ah* interface prefix (Linux IFNAMSIZ is 16).
func TapName(id sandbox.ID) string {
	s := sha256.Sum256([]byte(id))
	return "ah" + hex.EncodeToString(s[:6])
}

func (d *Driver) lock(ctx context.Context) (func(), error) {
	f, err := os.OpenFile(filepath.Join(d.cfg.DataDir, "driver.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		if err = delay(ctx, 20*time.Millisecond); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
}

func delay(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *Driver) load(id sandbox.ID) (*manifest, error) {
	if !validID(string(id)) {
		return nil, sandbox.ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(d.dir(id), "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, sandbox.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sandbox %s manifest: %w", id, err)
	}
	if !filepath.IsAbs(m.BaseImagePath) || m.Version != 1 || m.Spec.AgentID != string(id) || !m.Subnet.IsValid() || m.Subnet.Bits() != 30 || !d.cfg.NetworkPool.Contains(m.Subnet.Addr()) || m.Subnet != m.Subnet.Masked() || m.UID == 0 || m.GID == 0 {
		return nil, fmt.Errorf("sandbox %s has an invalid manifest or is outside the configured network pool", id)
	}
	return &m, nil
}

func (d *Driver) save(id sandbox.ID, m *manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(d.dir(id), "state.json"), data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (d *Driver) manifests() (map[sandbox.ID]*manifest, error) {
	entries, err := os.ReadDir(filepath.Join(d.cfg.DataDir, "agents"))
	if err != nil {
		return nil, err
	}
	out := map[sandbox.ID]*manifest{}
	used := map[netip.Prefix]sandbox.ID{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := sandbox.ID(e.Name())
		m, err := d.load(id)
		if errors.Is(err, sandbox.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if other, ok := used[m.Subnet]; ok {
			return nil, fmt.Errorf("sandboxes %s and %s share a subnet", id, other)
		}
		used[m.Subnet] = id
		out[id] = m
	}
	return out, nil
}

// Create implements sandbox.Driver.
func (d *Driver) Create(ctx context.Context, s sandbox.Spec) (result sandbox.ID, resultErr error) {
	if err := validateSpec(s); err != nil {
		return "", err
	}
	id := sandbox.ID(s.AgentID)
	unlock, err := d.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err = d.load(id); err == nil {
		return id, nil
	} else if !errors.Is(err, sandbox.ErrNotFound) {
		return "", err
	}
	all, err := d.manifests()
	if err != nil {
		return "", err
	}
	used := make([]netip.Prefix, 0, len(all))
	for _, m := range all {
		used = append(used, m.Subnet)
	}
	subnet, index, err := allocate(d.cfg.NetworkPool, used)
	if err != nil {
		return "", err
	}
	if len(d.socket(id)) > 107 {
		return "", errors.New("jailer API socket path exceeds 107 bytes; configure a shorter JailDir")
	}
	if err = os.MkdirAll(d.dir(id), 0o700); err != nil {
		return "", err
	}
	// Only remove files this attempt created. Destroy(KeepHome) may have left
	// valuable home data behind. A manifest saved before an fsync error owns its
	// disks and remains recoverable instead of being torn apart by rollback.
	var fresh []string
	for _, name := range []string{"home.ext4", "overlay.ext4"} {
		p := filepath.Join(d.dir(id), name)
		_, e := os.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			fresh = append(fresh, p)
		} else if e != nil {
			return "", e
		}
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if _, e := os.Stat(filepath.Join(d.dir(id), "state.json")); e == nil {
			return
		}
		for _, p := range fresh {
			_ = os.Remove(p)
		}
		_ = os.Remove(d.dir(id))
	}()
	m := &manifest{BaseImagePath: d.cfg.BaseImagePath, Version: 1, Spec: s, Subnet: subnet, UID: d.cfg.UIDBase + index, GID: d.cfg.GIDBase + index, State: sandbox.StateCreated}
	m.Spec.Env = maps.Clone(s.Env)
	overlay, home := capacities(d.cfg, s)
	// Kept home disks can be reused after Destroy(KeepHome), but never resized
	// implicitly. A failed Create cannot truncate existing user data.
	if err = d.provisionDisk(filepath.Join(d.dir(id), "home.ext4"), home, m.UID, m.GID); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = d.provisionDisk(filepath.Join(d.dir(id), "overlay.ext4"), overlay, m.UID, m.GID); err != nil {
		return "", err
	}
	if err = d.save(id, m); err != nil {
		return "", err
	}
	return id, nil
}

// Status implements sandbox.Driver.
func (d *Driver) Status(ctx context.Context, id sandbox.ID) (sandbox.Status, error) {
	unlock, err := d.lock(ctx)
	if err != nil {
		return sandbox.Status{}, err
	}
	defer unlock()
	m, err := d.load(id)
	if errors.Is(err, sandbox.ErrNotFound) {
		return sandbox.Status{State: sandbox.StateMissing}, nil
	}
	if err != nil {
		return sandbox.Status{}, err
	}
	running, err := d.running(m)
	if err != nil {
		return sandbox.Status{}, err
	}
	if running {
		state, e := d.guestState(ctx, id)
		if e != nil {
			return sandbox.Status{}, e
		}
		if state != "Running" && state != "Paused" {
			if e = d.kill(m); e != nil {
				return sandbox.Status{}, e
			}
			if e = d.waitExit(ctx, m); e != nil {
				return sandbox.Status{}, e
			}
			running = false
		}
	}
	if m.Process.PID != 0 && !running {
		if err = d.finish(ctx, id, m); err != nil {
			return sandbox.Status{}, err
		}
	}
	state := m.State
	if running {
		state = sandbox.StateRunning
	}
	return sandbox.Status{State: state, Subnet: m.Subnet, GatewayIP: m.Subnet.Addr().Next().String()}, nil
}

// Attach implements sandbox.Attacher.
func (d *Driver) Attach(ctx context.Context, id sandbox.ID) error {
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := d.load(id)
	if err != nil {
		return err
	}
	running, err := d.running(m)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	if err = d.ensureTap(ctx, id, m); err != nil {
		return err
	}
	if d.cfg.RegisterNetwork != nil {
		return d.cfg.RegisterNetwork(ctx, TapName(id), m.Subnet.Addr().Next().Next())
	}
	return nil
}

// Start implements sandbox.Driver.
func (d *Driver) Start(ctx context.Context, id sandbox.ID, boot sandbox.Boot) (err error) {
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := d.load(id)
	if err != nil {
		return err
	}
	running, err := d.running(m)
	if err != nil {
		return err
	}
	if running {
		state, e := d.guestState(ctx, id)
		if e != nil {
			return e
		}
		if state == "Running" || state == "Paused" {
			return nil
		}
		if e = d.kill(m); e != nil {
			return e
		}
		if e = d.waitExit(ctx, m); e != nil {
			return e
		}
	}
	if boot.AgentID != m.Spec.AgentID {
		return errors.New("boot agent id does not match sandbox")
	}
	if err = d.finish(ctx, id, m); err != nil {
		return err
	}
	// From here every failure unwinds mounts, network, cgroup and the config disk.
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			killErr := d.kill(m)
			if killErr == nil {
				killErr = d.waitExit(cleanupCtx, m)
			}
			if killErr == nil {
				killErr = d.finish(cleanupCtx, id, m)
			}
			err = errors.Join(err, killErr)
		}
	}()
	if err = d.prepare(ctx, id, m, boot); err != nil {
		return err
	}
	if err = d.ensureTap(ctx, id, m); err != nil {
		return err
	}
	if d.cfg.RegisterNetwork != nil {
		if err = d.cfg.RegisterNetwork(ctx, TapName(id), m.Subnet.Addr().Next().Next()); err != nil {
			return err
		}
	}
	return d.launch(ctx, id, m)
}

// Stop implements sandbox.Driver.
func (d *Driver) Stop(ctx context.Context, id sandbox.ID) error {
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := d.load(id)
	if err != nil {
		return err
	}
	return d.stop(ctx, id, m)
}

func (d *Driver) stop(ctx context.Context, id sandbox.ID, m *manifest) error {
	running, err := d.running(m)
	if err != nil {
		return err
	}
	if !running {
		return d.finish(ctx, id, m)
	}
	grace, cancel := context.WithTimeout(ctx, d.cfg.StopTimeout)
	defer cancel()
	var requested bool
	if d.cfg.Shutdown != nil {
		requested = d.cfg.Shutdown(grace, id) == nil
	}
	if !requested && grace.Err() == nil {
		a := newAPI(d.socket(id))
		_ = a.action(grace, "SendCtrlAltDel")
		a.close()
	}
	waitErr := d.waitExit(grace, m)
	// A caller cancellation still must not leave an untracked, running VM.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	if waitErr != nil {
		if err = d.kill(m); err != nil {
			return errors.Join(waitErr, err)
		}
		if err = d.waitExit(cleanupCtx, m); err != nil {
			return errors.Join(waitErr, err)
		}
	}
	err = d.finish(cleanupCtx, id, m)
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	// Exceeding the shutdown grace is a successful forced stop. Disks are kept;
	// ext4's journal is recovered by sandboxd on the next cold boot.
	return err
}

// Destroy implements sandbox.Driver.
func (d *Driver) Destroy(ctx context.Context, id sandbox.ID, opts sandbox.DestroyOpts) error {
	unlock, err := d.lock(ctx)
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
	if err = d.stop(ctx, id, m); err != nil {
		return err
	}
	if !opts.KeepHome {
		return os.RemoveAll(d.dir(id))
	}
	for _, name := range []string{"overlay.ext4", "serial.log", "state.json"} {
		if err = os.Remove(filepath.Join(d.dir(id), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Rebuild resets only the system disk. The persistent home file is never
// replaced, copied, formatted or shrunk by this operation.
func (d *Driver) Rebuild(ctx context.Context, id sandbox.ID, s sandbox.Spec) error {
	if err := validateSpec(s); err != nil {
		return err
	}
	if string(id) != s.AgentID {
		return errors.New("reset system: sandbox id does not match agent")
	}
	unlock, err := d.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := d.load(id)
	if err != nil {
		return err
	}
	if err = d.stop(ctx, id, m); err != nil {
		return err
	}
	overlay, home := capacities(d.cfg, s)
	st, err := os.Stat(filepath.Join(d.dir(id), "home.ext4"))
	if err != nil {
		return err
	}
	if s.Resources.DiskGB > 0 && st.Size() > home {
		return errors.New("reset system cannot shrink the persistent home disk")
	}
	pending := filepath.Join(d.dir(id), "overlay.pending")
	_ = os.Remove(pending)
	if err = d.provisionDisk(pending, overlay, m.UID, m.GID); err != nil {
		return err
	}
	defer os.Remove(pending)
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(pending, filepath.Join(d.dir(id), "overlay.ext4")); err != nil {
		return err
	}
	m.BaseImagePath = d.cfg.BaseImagePath
	m.Spec = s
	m.Spec.Env = maps.Clone(s.Env)
	m.State = sandbox.StateStopped
	return d.save(id, m)
}
