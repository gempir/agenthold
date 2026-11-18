package sandboxd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// The host delivers sandboxd to the guest as an initramfs that contains only
// the daemon as /init (plus the directories it mounts on). The kernel unpacks
// it into its rootfs and starts /init; RunInit then mounts the base image and
// switches into it. The base image itself carries no sandboxd: whatever an old
// base has at SandboxdPath is covered by a bind mount of the delivered binary.
const (
	// InitramfsInit is the delivered binary's path inside the initramfs.
	InitramfsInit = "/init"
	// SandboxdPath is where the delivered sandboxd is visible in the running
	// guest (bind-mounted over the base image's file). Harness command lines
	// (the platform MCP server) and the daemon child use it.
	SandboxdPath = "/opt/agenthold/sandboxd"
)

// WriteInitramfs writes an uncompressed newc cpio archive with sandboxd
// (size bytes read from r) as /init. The archive is deterministic: the same
// binary gives the same bytes. Every kernel with CONFIG_BLK_DEV_INITRD unpacks
// it; no decompressor is needed.
func WriteInitramfs(w io.Writer, r io.Reader, size int64) error {
	if size <= 0 || size > 1<<30 {
		return fmt.Errorf("initramfs: invalid sandboxd size %d", size)
	}
	c := &cpioWriter{w: w}
	for _, d := range []string{"dev", "proc", "sys", "run"} {
		c.header(d, 0o040755, 2, 0, 0, 0)
	}
	// The kernel opens /dev/console for init's stdio before running it.
	c.header("dev/console", 0o020600, 1, 0, 5, 1)
	c.header(InitramfsInit[1:], 0o100755, 1, size, 0, 0)
	if c.err == nil {
		var n int64
		n, c.err = io.Copy(c.w, io.LimitReader(r, size))
		if c.err == nil && n != size {
			c.err = errors.New("initramfs: sandboxd is shorter than its size")
		}
		c.n += n
		c.pad()
	}
	c.header("TRAILER!!!", 0, 1, 0, 0, 0)
	return c.err
}

type cpioWriter struct {
	w   io.Writer
	n   int64
	ino int
	err error
}

func (c *cpioWriter) write(b []byte) {
	if c.err != nil {
		return
	}
	var n int
	n, c.err = c.w.Write(b)
	c.n += int64(n)
}

func (c *cpioWriter) pad() {
	if r := c.n % 4; r != 0 {
		c.write(make([]byte, 4-r))
	}
}

// header writes one newc entry header and name; the caller writes the data.
func (c *cpioWriter) header(name string, mode uint32, nlink int, size int64, rmajor, rminor int) {
	ino := 0
	if name != "TRAILER!!!" {
		c.ino++
		ino = c.ino
	}
	var b bytes.Buffer
	// magic, ino, mode, uid, gid, nlink, mtime, filesize, devmajor, devminor,
	// rdevmajor, rdevminor, namesize, check
	fmt.Fprintf(&b, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		ino, mode, 0, 0, nlink, 0, size, 0, 0, rmajor, rminor, len(name)+1, 0)
	b.WriteString(name)
	b.WriteByte(0)
	c.write(b.Bytes())
	c.pad()
}
