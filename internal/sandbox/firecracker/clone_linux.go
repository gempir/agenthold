//go:build linux

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"

	"github.com/gempir/agenthold/internal/diskcopy"
	"github.com/gempir/agenthold/internal/sandbox"
)

// diskNames are the persistent disks of a sandbox, copied by Clone.
var diskNames = []string{"overlay.ext4", "home.ext4"}

var errCloning = errors.New("sandbox is being cloned")

func (d *Driver) beginClone(id sandbox.ID) {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	if d.cloning == nil {
		d.cloning = map[sandbox.ID]int{}
	}
	d.cloning[id]++
}

func (d *Driver) endClone(id sandbox.ID) {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	if d.cloning[id]--; d.cloning[id] <= 0 {
		delete(d.cloning, id)
	}
}

// notCloning fails while a Clone reads id's disks.
func (d *Driver) notCloning(id sandbox.ID) error {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	if d.cloning[id] > 0 {
		return fmt.Errorf("%w: %s", errCloning, id)
	}
	return nil
}

// claimDestination makes Clone calls for one destination exclusive. A clone
// that was cancelled (for example when the node connection dropped) may still
// be unwinding, which removes the directory: a clone started meanwhile for the
// same id waits for it to finish, instead of having its copy removed.
func (d *Driver) claimDestination(ctx context.Context, dst sandbox.ID) (release func(), err error) {
	for {
		d.cloningMu.Lock()
		if d.destinations == nil {
			d.destinations = map[sandbox.ID]chan struct{}{}
		}
		busy, ok := d.destinations[dst]
		if !ok {
			done := make(chan struct{})
			d.destinations[dst] = done
			d.cloningMu.Unlock()
			return func() {
				d.cloningMu.Lock()
				delete(d.destinations, dst)
				d.cloningMu.Unlock()
				close(done)
			}, nil
		}
		d.cloningMu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (d *Driver) chownDisk(path string, uid, gid uint32) error {
	if d.chown != nil {
		return d.chown(path, int(uid), int(gid))
	}
	return os.Chown(path, int(uid), int(gid))
}

// Clone implements sandbox.Driver. The copy of the disks, which can take a
// while without reflinks, runs without the driver lock; the source is protected
// by an in-memory marker instead (Start, Rebuild and Destroy refuse). The new
// sandbox appears atomically: disks are copied under temporary names, and only
// the manifest, written last, makes it exist. A crash leaves unreferenced
// temporary files in the destination directory, which the next Clone for the
// same id removes.
func (d *Driver) Clone(ctx context.Context, src sandbox.ID, s sandbox.Spec) (result sandbox.ID, resultErr error) {
	if err := validateSpec(s); err != nil {
		return "", err
	}
	dst := sandbox.ID(s.AgentID)
	if dst == src {
		return "", errors.New("clone: source and destination are the same sandbox")
	}
	release, err := d.claimDestination(ctx, dst)
	if err != nil {
		return "", err
	}
	defer release()
	unlock, err := d.lockIdle(ctx, src)
	if err != nil {
		return "", err
	}
	if _, err = d.load(dst); err == nil {
		unlock()
		return dst, nil
	} else if !errors.Is(err, sandbox.ErrNotFound) {
		unlock()
		return "", err
	}
	m, err := d.load(src)
	if err == nil {
		err = d.requireStopped(m)
	}
	if err != nil {
		unlock()
		return "", err
	}
	if len(d.socket(dst)) > 107 {
		unlock()
		return "", errors.New("jailer API socket path exceeds 107 bytes; configure a shorter JailDir")
	}
	d.beginClone(src)
	unlock()
	defer d.endClone(src)

	dir := d.dir(dst)
	// dst has no manifest, so anything in its directory is the debris of an
	// interrupted attempt of this very clone (ids are never reused for disks).
	if err = os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err = syncDir(filepath.Dir(dir)); err != nil {
		return "", err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range diskNames {
		if _, err = diskcopy.Copy(ctx, filepath.Join(d.dir(src), name), filepath.Join(dir, name+".cloning"), diskcopy.Options{}); err != nil {
			return "", fmt.Errorf("clone %s: %w", name, err)
		}
	}

	unlock, err = d.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err = d.load(dst); err == nil {
		return "", fmt.Errorf("sandbox %s appeared while it was being cloned", dst)
	} else if !errors.Is(err, sandbox.ErrNotFound) {
		return "", err
	}
	// The source may have been removed or started meanwhile; start is refused
	// while cloning, but a VMM outside the driver's control is not.
	if m, err = d.load(src); err == nil {
		err = d.requireStopped(m)
	}
	if err != nil {
		return "", err
	}
	all, err := d.manifests()
	if err != nil {
		return "", err
	}
	used := make([]netip.Prefix, 0, len(all))
	for _, other := range all {
		used = append(used, other.Subnet)
	}
	subnet, index, err := allocate(d.cfg.NetworkPool, used)
	if err != nil {
		return "", err
	}
	clone := &manifest{
		// The system disk is an overlay on the base image and layers it was built with.
		BaseImagePath: m.BaseImagePath, Layers: slices.Clone(m.Layers), Version: 1, Spec: s, Subnet: subnet,
		UID: d.cfg.UIDBase + index, GID: d.cfg.GIDBase + index, State: sandbox.StateCreated,
		// Its first boot clears the identity the copied disks carry.
		Fresh: true,
	}
	clone.Spec.Env = maps.Clone(s.Env)
	clone.Spec.Layers = slices.Clone(s.Layers)
	for _, name := range diskNames {
		if err = d.chownDisk(filepath.Join(dir, name+".cloning"), clone.UID, clone.GID); err != nil {
			return "", err
		}
	}
	for _, name := range diskNames {
		if err = os.Rename(filepath.Join(dir, name+".cloning"), filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	if err = syncDir(dir); err != nil {
		return "", err
	}
	if err = d.save(dst, clone); err != nil {
		return "", err
	}
	return dst, nil
}

func (d *Driver) requireStopped(m *manifest) error {
	running, err := d.running(m)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%w: %s", sandbox.ErrRunning, m.Spec.AgentID)
	}
	return nil
}
