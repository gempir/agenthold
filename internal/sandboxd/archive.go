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

// Archive limits when the request sets none: the file contents (fs.tar and
// fs.untar) and the number of entries.
const (
	DefaultArchiveMax int64 = 2 << 30
	MaxArchiveEntries       = 200_000
)

// ErrArchiveTooLarge is returned when an archive exceeds a limit.
var ErrArchiveTooLarge = errors.New("archive is larger than the limit")

// archiveDeny are never archived: sandboxd's own state (boot token) and
// kernel filesystems.
var archiveDeny = []string{"/etc/agenthold", InitRun, "/proc", "/sys", "/dev"}

// WriteTar writes the tree at dir to w as a tar archive without following
// symlinks. Files are opened through an os.Root of dir, so a file swapped for
// a symlink while the archive is written cannot make it read outside dir.
// An exclude without a slash matches that name at any depth (".git"), one
// with a slash a path relative to dir ("build/cache"). Sockets, devices and
// FIFOs are left out. The file contents may add up to at most maxBytes, the
// entries to MaxArchiveEntries.
func WriteTar(w io.Writer, dir string, exclude []string, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveMax
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if real == "/" {
		return fmt.Errorf("%s cannot be archived", dir)
	}
	for _, p := range archiveDeny {
		if real == p || strings.HasPrefix(real, p+"/") {
			return fmt.Errorf("%s cannot be archived", dir)
		}
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return err
	}
	defer root.Close()
	tw := tar.NewWriter(w)
	var total int64
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if excluded(rel, exclude) {
			if e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode().IsRegular(), info.IsDir():
		case info.Mode()&os.ModeSymlink != 0:
			if link, err = root.Readlink(rel); err != nil {
				return err
			}
		default:
			return nil
		}
		if entries++; entries > MaxArchiveEntries {
			return fmt.Errorf("%w: more than %d entries", ErrArchiveTooLarge, MaxArchiveEntries)
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
		// O_NOFOLLOW-like: a symlink swapped in now fails the size check
		// below or stays inside the root.
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s changed while it was archived", rel)
		}
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

// ExtractTar extracts a tar archive into dir (created if missing; it must be
// empty). It is ExtractTarRoot on an os.Root of dir.
func ExtractTar(r io.Reader, dir string, maxBytes int64, own func(root *os.Root, name string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return ExtractTarRoot(r, root, maxBytes, own)
}

// ExtractTarRoot extracts a tar archive into root, which must be empty. Every
// file operation goes through the os.Root, so no entry (and no symlink swapped
// in meanwhile) can make it write outside. Absolute names, "..", and paths
// through symlinks are refused (symlinks themselves are created as they are).
// Only directories, regular files and symlinks are allowed; permission bits
// are kept without setuid, setgid and sticky. own is called on every entry.
// The file contents may add up to maxBytes, the entries to MaxArchiveEntries.
func ExtractTarRoot(r io.Reader, root *os.Root, maxBytes int64, own func(root *os.Root, name string)) error {
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveMax
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(1)
	_ = d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("%s is not empty", root.Name())
	}
	tr := tar.NewReader(r)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entries++; entries > MaxArchiveEntries {
			return fmt.Errorf("%w: more than %d entries", ErrArchiveTooLarge, MaxArchiveEntries)
		}
		name := path.Clean(strings.TrimSuffix(hdr.Name, "/"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry %q leaves the target directory", hdr.Name)
		}
		if err := safeParents(root, name, own); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			fi, err := root.Lstat(name)
			switch {
			case err == nil && !fi.IsDir():
				return fmt.Errorf("archive entry %q: a file is in the way", hdr.Name)
			case err != nil:
				if err := root.Mkdir(name, 0o755); err != nil {
					return err
				}
			}
			own(root, name)
			if err := root.Chmod(name, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += hdr.Size; total > maxBytes {
				return fmt.Errorf("%w (%d bytes)", ErrArchiveTooLarge, maxBytes)
			}
			if err := removeIfNotDir(root, name); err != nil {
				return err
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
			own(root, name)
			if err := root.Chmod(name, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := removeIfNotDir(root, name); err != nil {
				return err
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
			own(root, name)
		default:
			return fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

// safeParents creates the missing parents of name and refuses a parent that
// is not a directory (a symlink in particular).
func safeParents(root *os.Root, name string, own func(*os.Root, string)) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	cur := ""
	for part := range strings.SplitSeq(parent, "/") {
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := root.Mkdir(cur, 0o755); err != nil {
				return err
			}
			own(root, cur)
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("archive path %s goes through a symlink or file", cur)
		}
	}
	return nil
}

func removeIfNotDir(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", name)
	}
	return root.Remove(name)
}

// homeRel turns a request path into a path relative to the agent's home and
// refuses one outside it.
func (d *Daemon) homeRel(p string) (string, error) {
	rel, err := filepath.Rel(d.acct.home, d.resolve(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s must be inside the home directory", p)
	}
	return rel, nil
}

// ownIn gives a path inside root to the agent account (without following a
// final symlink, and through the root).
func (d *Daemon) ownIn(root *os.Root, name string) {
	if d.acct.switchTo {
		_ = root.Lchown(name, d.acct.uid, d.acct.gid)
	}
}

// mkdirAllIn creates rel and its missing parents inside root, owned by the
// agent account.
func (d *Daemon) mkdirAllIn(root *os.Root, rel string) error {
	cur := ""
	for part := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := root.Mkdir(cur, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			d.ownIn(root, cur)
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// fsTar streams a directory out. sandboxd is root in the guest (the agent
// account has passwordless sudo there, so reading as root grants nothing
// new); sandboxd's own state is never archived.
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

// fsUntar extracts into a new directory inside the home. sandboxd writes as
// root through an os.Root of the home and gives every entry to the agent
// account, so symlinks the agent placed (or swaps in meanwhile) cannot
// redirect the writes outside the home.
func (d *Daemon) fsUntar(st net.Conn, o tunnel.Open) error {
	rel, err := d.homeRel(o.Path)
	if err != nil {
		return err
	}
	home, err := os.OpenRoot(d.acct.home)
	if err != nil {
		return err
	}
	defer home.Close()
	if err := d.mkdirAllIn(home, rel); err != nil {
		return err
	}
	root, err := home.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := tunnel.WriteJSON(st, tunnel.OK{OK: true}); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := tunnel.CopyData(pw, st, nil)
		_ = pw.CloseWithError(err)
	}()
	err = ExtractTarRoot(pr, root, o.MaxBytes, d.ownIn)
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

// fsSymlink points the symlink at o.Path (inside the home) to o.Target,
// replacing it atomically, through an os.Root of the home.
func (d *Daemon) fsSymlink(st net.Conn, o tunnel.Open) error {
	rel, err := d.homeRel(o.Path)
	if err != nil {
		return err
	}
	if o.Target == "" {
		return errors.New("fs.symlink: a target is required")
	}
	home, err := os.OpenRoot(d.acct.home)
	if err != nil {
		return err
	}
	defer home.Close()
	if fi, err := home.Lstat(rel); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory", o.Path)
	}
	tmp := filepath.Join(filepath.Dir(rel), ".agenthold-link-"+filepath.Base(rel))
	_ = home.Remove(tmp)
	if err := home.Symlink(o.Target, tmp); err != nil {
		return err
	}
	d.ownIn(home, tmp)
	if err := home.Rename(tmp, rel); err != nil {
		_ = home.Remove(tmp)
		return err
	}
	return tunnel.WriteJSON(st, tunnel.OK{OK: true})
}
