//go:build linux

package firecracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gempir/agenthold/internal/sandboxd"
)

// writeInitrd builds the initramfs that delivers sandboxd to every guest
// (dataDir/initrd/<sha256>.cpio, content-addressed by the archive) and removes
// older archives. A running VM keeps the archive it booted with through the
// bind mount in its jail, so removing the file never affects it.
func writeInitrd(dataDir, sandboxdPath string) (string, error) {
	bin, err := os.ReadFile(sandboxdPath)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := sandboxd.WriteInitramfs(&buf, bytes.NewReader(bin), int64(len(bin))); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	dir := filepath.Join(dataDir, "initrd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := hex.EncodeToString(sum[:]) + ".cpio"
	path := filepath.Join(dir, name)
	if have, err := os.ReadFile(path); err != nil || !bytes.Equal(have, buf.Bytes()) {
		if err := atomicWrite(path, buf.Bytes(), 0o644); err != nil {
			return "", err
		}
	}
	// The jailed VMM user reads it through the bind mount.
	if err := os.Chmod(path, 0o644); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() != name && strings.HasSuffix(e.Name(), ".cpio") && e.Type().IsRegular() {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != int64(buf.Len()) {
		return "", fmt.Errorf("initramfs %s was not written", path)
	}
	return path, nil
}
