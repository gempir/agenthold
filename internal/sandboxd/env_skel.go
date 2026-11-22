package sandboxd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BuildChildEnv builds the daemon's environment: defaults, then baseEnvFile
// (/etc/agenthold/image.env), then sorted *.env files under envDir
// (/etc/agenthold/image.env.d), then the host's per-boot variables.
//
// In envDir drop-ins, a PATH assignment may reference $PATH or ${PATH} to
// prepend or append without clobbering the accumulated PATH.
func BuildChildEnv(baseEnvFile, envDir string, boot map[string]string, log *slog.Logger) []string {
	env := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG": "C.UTF-8",
	}
	if baseEnvFile != "" {
		if f, err := os.Open(baseEnvFile); err == nil {
			img, err := ParseEnvFile(f)
			_ = f.Close()
			if err != nil && log != nil {
				log.Warn("image env", "file", baseEnvFile, "err", err)
			}
			maps.Copy(env, img)
		}
	}
	if envDir != "" {
		if entries, err := os.ReadDir(envDir); err == nil {
			// ReadDir returns entries sorted by name.
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".env") {
					continue
				}
				p := filepath.Join(envDir, entry.Name())
				f, err := os.Open(p)
				if err != nil {
					if log != nil {
						log.Warn("image env drop-in", "file", p, "err", err)
					}
					continue
				}
				img, err := ParseEnvFile(f)
				_ = f.Close()
				if err != nil && log != nil {
					log.Warn("image env drop-in", "file", p, "err", err)
				}
				for k, v := range img {
					if k == "PATH" {
						v = expandPath(v, env["PATH"])
					}
					env[k] = v
				}
			}
		}
	}
	maps.Copy(env, boot)
	env[EnvInitChild] = "1"
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func expandPath(val, current string) string {
	return strings.NewReplacer("${PATH}", current, "$PATH", current).Replace(val)
}

// ProfilePathScript returns an /etc/profile.d snippet that restores the
// resolved PATH after Debian's /etc/profile overwrites it in login shells.
func ProfilePathScript(path string) string {
	quoted := "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	return "# managed by agenthold (rewritten at every boot)\nexport PATH=" + quoted + "\n"
}

// SeedHomeFromSkel copies regular files, directories, and symlinks from skelDir
// into homeDir without overwriting anything that already exists. When uid and
// gid are non-negative, copied entries are chowned to uid:gid. A missing
// skelDir is a no-op.
func SeedHomeFromSkel(skelDir, homeDir string, uid, gid int) error {
	info, err := os.Stat(skelDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", skelDir, err)
	}
	if !info.IsDir() {
		return nil
	}
	return copySkelEntries(skelDir, homeDir, uid, gid)
}

func copySkelEntries(skelDir, homeDir string, uid, gid int) error {
	entries, err := os.ReadDir(skelDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(skelDir, entry.Name())
		dst := filepath.Join(homeDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if !mode.IsDir() && !mode.IsRegular() && mode&os.ModeSymlink == 0 {
			continue
		}
		existing, err := os.Lstat(dst)
		if err == nil {
			if mode.IsDir() && existing.IsDir() {
				if err := copySkelEntries(src, dst, uid, gid); err != nil {
					return err
				}
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		perm := mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		switch {
		case mode.IsDir():
			// Populate before applying ownership and potentially read-only permissions.
			if err := os.Mkdir(dst, 0o700); err != nil {
				return err
			}
			if err := copySkelEntries(src, dst, uid, gid); err != nil {
				return err
			}
			if uid >= 0 && gid >= 0 {
				if err := os.Chown(dst, uid, gid); err != nil {
					return err
				}
			}
			if err := os.Chmod(dst, perm); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
			if uid >= 0 && gid >= 0 {
				if err := os.Lchown(dst, uid, gid); err != nil {
					return err
				}
			}
		case mode.IsRegular():
			if err := copySkelFile(src, dst, perm, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

func copySkelFile(src, dst string, perm fs.FileMode, uid, gid int) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	var chownErr error
	if uid >= 0 && gid >= 0 {
		chownErr = out.Chown(uid, gid)
	}
	chmodErr := out.Chmod(perm)
	closeErr := out.Close()
	if err := errors.Join(copyErr, chmodErr, chownErr, closeErr); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
