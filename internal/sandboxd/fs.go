package sandboxd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gempir/agenthold/internal/tunnel"
)

// resolve turns a request path into an absolute path. Relative paths are
// relative to the agent's home. The whole sandbox is the agent's computer, so
// absolute paths are allowed; access control happens on the server.
func (d *Daemon) resolve(p string) string {
	if p == "" || p == "~" {
		return d.acct.home
	}
	if len(p) > 1 && p[:2] == "~/" {
		p = filepath.Join(d.acct.home, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(d.acct.home, p)
	}
	return filepath.Clean(p)
}

func (d *Daemon) own(path string) {
	if d.acct.switchTo {
		_ = os.Lchown(path, d.acct.uid, d.acct.gid)
	}
}

func (d *Daemon) fsList(st net.Conn, o tunnel.Open) error {
	dir := d.resolve(o.Path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	out := tunnel.ListReply{Path: dir, Entries: make([]tunnel.FileInfo, 0, len(entries))}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fi := tunnel.FileInfo{
			Name: e.Name(), Size: info.Size(), Mode: info.Mode().String(), IsDir: info.IsDir(),
			IsLink: info.Mode()&os.ModeSymlink != 0, ModTime: info.ModTime().Unix(),
		}
		if fi.IsLink {
			if target, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				fi.IsDir = target.IsDir()
			}
			fi.Target, _ = os.Readlink(filepath.Join(dir, e.Name()))
		}
		out.Entries = append(out.Entries, fi)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	return tunnel.WriteJSON(st, out)
}

// denied reports whether path (after resolving symlinks) is under a denied path.
func (d *Daemon) denied(path string, deny []string) bool {
	if len(deny) == 0 {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	for _, p := range deny {
		p = d.resolve(p)
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
		if real == p || strings.HasPrefix(real, p+"/") {
			return true
		}
	}
	return false
}

var errDenied = errors.New("access to this path is restricted to agent admins")

func (d *Daemon) fsRead(st net.Conn, o tunnel.Open) error {
	path := d.resolve(o.Path)
	if d.denied(path, o.Deny) {
		return errDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if err := tunnel.WriteJSON(st, tunnel.ReadReply{Size: info.Size()}); err != nil {
		return err
	}
	if _, err := io.Copy(tunnel.DataWriter(st), f); err != nil {
		return err
	}
	return tunnel.WriteFrame(st, tunnel.FrameEOF, nil)
}

func (d *Daemon) fsWrite(st net.Conn, o tunnel.Open) error {
	path := d.resolve(o.Path)
	mode := o.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := d.mkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agenthold-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tunnel.CopyData(tmp, st, nil); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	d.own(tmp.Name())
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}

// mkdirAll creates missing directories owned by the agent account.
func (d *Daemon) mkdirAll(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	if err := d.mkdirAll(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	d.own(dir)
	return nil
}

func (d *Daemon) fsMkdir(st net.Conn, o tunnel.Open) error {
	path := d.resolve(o.Path)
	if o.Recursive {
		if err := d.mkdirAll(path); err != nil {
			return err
		}
	} else {
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		d.own(path)
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}

func (d *Daemon) fsRemove(st net.Conn, o tunnel.Open) error {
	path := d.resolve(o.Path)
	if path == "/" || path == d.acct.home {
		return fmt.Errorf("refusing to remove %s", path)
	}
	var err error
	if o.Recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return err
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}
