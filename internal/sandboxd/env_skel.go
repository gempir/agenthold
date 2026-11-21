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
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".env") {
					continue
				}
				names = append(names, e.Name())
			}
			slices.Sort(names)
			for _, name := range names {
				p := filepath.Join(envDir, name)
				st, err := os.Stat(p)
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
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
	val = strings.ReplaceAll(val, "${PATH}", current)
	val = strings.ReplaceAll(val, "$PATH", current)
	return val
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
	return filepath.WalkDir(skelDir, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(skelDir, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(homeDir, rel)
		fi, err := os.Lstat(src)
		if err != nil {
			return err
		}
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			if existing, err := os.Lstat(dst); err == nil {
				if existing.IsDir() {
					return nil
				}
				return filepath.SkipDir
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Mkdir(dst, mode.Perm()); err != nil {
				return err
			}
			if uid >= 0 && gid >= 0 {
				if err := os.Chown(dst, uid, gid); err != nil {
					return err
				}
			}
		case mode&os.ModeSymlink != 0:
			if _, err := os.Lstat(dst); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
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
			if _, err := os.Lstat(dst); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := copySkelFile(src, dst, mode.Perm(), uid, gid); err != nil {
				return err
			}
		}
		return nil
	})
}

func copySkelFile(src, dst string, perm fs.FileMode, uid, gid int) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	chmodErr := out.Chmod(perm)
	var chownErr error
	if uid >= 0 && gid >= 0 {
		chownErr = out.Chown(uid, gid)
	}
	closeErr := out.Close()
	if err := errors.Join(copyErr, chmodErr, chownErr, closeErr); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
