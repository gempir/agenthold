package sandboxd

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gempir/agenthold/internal/tunnel"
)

// DefaultArchiveMax bounds fs.tar and fs.untar when the request sets no limit.
const DefaultArchiveMax int64 = 2 << 30

// ErrArchiveTooLarge is returned when an archive exceeds its limit.
var ErrArchiveTooLarge = errors.New("archive is larger than the limit")

// archiveDeny are never archived: sandboxd's own state (boot token) and
// kernel filesystems.
var archiveDeny = []string{"/etc/agenthold", InitRun, "/proc", "/sys", "/dev"}

// WriteTar writes the tree at dir to w as a tar archive without following
// symlinks. An exclude without a slash matches that name at any depth (".git"),
// one with a slash a path relative to dir ("build/cache"). Sockets, devices and
// FIFOs are left out. The file contents may add up to at most maxBytes.
func WriteTar(w io.Writer, dir string, exclude []string, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveMax
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for _, p := range archiveDeny {
		if real == p || strings.HasPrefix(real, p+"/") || real == "/" {
			return fmt.Errorf("%s cannot be archived", dir)
		}
	}
	tw := tar.NewWriter(w)
	var total int64
	err = filepath.WalkDir(real, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(real, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded(rel, exclude) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode().IsRegular(), info.IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		default:
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		hdr.Format = tar.FormatPAX
		if info.Mode().IsRegular() {
			if total += info.Size(); total > maxBytes {
				return fmt.Errorf("%w (%d bytes)", ErrArchiveTooLarge, maxBytes)
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		// A file growing while it is archived is cut at its header's size.
		_, err = io.CopyN(tw, f, info.Size())
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func excluded(rel string, exclude []string) bool {
	for _, ex := range exclude {
		ex = strings.Trim(path.Clean("/"+ex), "/")
		if ex == "" {
			continue
		}
		if strings.Contains(ex, "/") {
			if rel == ex || strings.HasPrefix(rel, ex+"/") {
				return true
			}
			continue
		}
		for part := range strings.SplitSeq(rel, "/") {
			if part == ex {
				return true
			}
		}
	}
	return false
}

// ExtractTar extracts a tar archive into dir, which is created if missing and
// must be empty. Entries must stay inside dir: absolute names, ".." and paths
// through symlinks are refused (symlinks themselves are created as they are).
// Only directories, regular files and symlinks are allowed; permission bits
// are kept without setuid, setgid and sticky. own is called on every entry.
func ExtractTar(r io.Reader, dir string, maxBytes int64, own func(string)) error {
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveMax
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if entries, err := os.ReadDir(dir); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimSuffix(hdr.Name, "/"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry %q leaves the target directory", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := safeParents(root, target, own); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			fi, err := os.Lstat(target)
			switch {
			case err == nil && !fi.IsDir():
				return fmt.Errorf("archive entry %q: a file is in the way", hdr.Name)
			case err != nil:
				if err := os.Mkdir(target, 0o755); err != nil {
					return err
				}
			}
			own(target)
			if err := os.Chmod(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += hdr.Size; total > maxBytes {
				return fmt.Errorf("%w (%d bytes)", ErrArchiveTooLarge, maxBytes)
			}
			if err := removeIfNotDir(target); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			own(target)
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := removeIfNotDir(target); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			own(target)
		default:
			return fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

// safeParents creates the missing parents of target below root and refuses
// a parent that is not a directory (a symlink in particular).
func safeParents(root, target string, own func(string)) error {
	rel, err := filepath.Rel(root, filepath.Dir(target))
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
			own(cur)
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("archive path %s goes through a symlink or file", cur)
		}
	}
	return nil
}

func removeIfNotDir(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", p)
	}
	return os.Remove(p)
}

func (d *Daemon) fsTar(st net.Conn, o tunnel.Open) error {
	dir := d.resolve(o.Path)
	if fi, err := os.Stat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := tunnel.WriteJSON(st, tunnel.OK{OK: true}); err != nil {
		return err
	}
	if err := WriteTar(tunnel.DataWriter(st), dir, o.Exclude, o.MaxBytes); err != nil {
		return err
	}
	return tunnel.WriteFrame(st, tunnel.FrameEOF, nil)
}

func (d *Daemon) fsUntar(st net.Conn, o tunnel.Open) error {
	dir := d.resolve(o.Path)
	if dir == d.acct.home || dir == "/" {
		return fmt.Errorf("refusing to extract into %s", dir)
	}
	if err := d.mkdirAll(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := tunnel.WriteJSON(st, tunnel.OK{OK: true}); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := tunnel.CopyData(pw, st, nil)
		_ = pw.CloseWithError(err)
	}()
	err := ExtractTar(pr, dir, o.MaxBytes, d.own)
	if err == nil {
		// Drain to the EOF frame (and padding the tar reader did not need).
		_, err = io.Copy(io.Discard, pr)
	}
	_ = pr.CloseWithError(errors.New("extraction ended"))
	if err != nil {
		return err
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}

// fsSymlink points the symlink at o.Path to o.Target, replacing it atomically.
func (d *Daemon) fsSymlink(st net.Conn, o tunnel.Open) error {
	link := d.resolve(o.Path)
	if o.Target == "" || link == d.acct.home || link == "/" {
		return errors.New("fs.symlink: path and target are required")
	}
	if fi, err := os.Lstat(link); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory", link)
	}
	tmp := filepath.Join(filepath.Dir(link), ".agenthold-link-"+filepath.Base(link))
	_ = os.Remove(tmp)
	if err := os.Symlink(o.Target, tmp); err != nil {
		return err
	}
	d.own(tmp)
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}
