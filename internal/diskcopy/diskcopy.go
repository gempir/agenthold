// Package diskcopy copies disk image files the cheap way: as a reflink (a
// copy-on-write clone, instant and sharing blocks) when the filesystem
// supports it, otherwise as a sparse copy that moves only the allocated data
// and keeps holes. Disks of sandboxes are sparse ext4 images, so a naive copy
// would turn a few hundred MiB of data into the full nominal size.
package diskcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Method says how a copy was made.
type Method string

// Methods.
const (
	Reflink Method = "reflink"
	Sparse  Method = "sparse"
)

// Options tune a copy.
type Options struct {
	// NoReflink skips the reflink attempt and always copies.
	NoReflink bool
}

const (
	chunkSize = 1 << 20
	// blockSize is the granularity at which all-zero data is turned into a hole.
	blockSize = 4096
)

// chunkHook runs after every copied chunk (tests).
var chunkHook func()

// Copy copies the regular file src to a new file dst (mode 0600, which must
// not exist). The copy is complete and synced when Copy returns nil; on any
// error, including cancellation, dst is removed. It checks ctx between chunks.
func Copy(ctx context.Context, src, dst string, opts Options) (m Method, err error) {
	if err = ctx.Err(); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, out.Close())
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if !opts.NoReflink {
		// A failed FICLONE leaves dst untouched, so any failure (unsupported
		// filesystem, different filesystem, ...) falls back to copying.
		if reflink(out, in) == nil {
			return Reflink, out.Sync()
		}
	}
	if err = out.Truncate(st.Size()); err != nil {
		return "", err
	}
	if err = sparseCopy(ctx, in, out, st.Size()); err != nil {
		return "", err
	}
	return Sparse, out.Sync()
}

// sparseCopy copies the data extents of in and leaves the holes of dst alone.
// dst already has its final size.
func sparseCopy(ctx context.Context, in, out *os.File, size int64) error {
	buf := make([]byte, chunkSize)
	var off int64
	for off < size {
		data, err := in.Seek(off, unix.SEEK_DATA)
		switch {
		case errors.Is(err, unix.ENXIO):
			return nil // only a hole left
		case err != nil:
			// The filesystem cannot tell data from holes: everything is data.
			// All-zero blocks are still skipped below.
			data = off
		}
		end := size
		if hole, err := in.Seek(data, unix.SEEK_HOLE); err == nil && hole < size {
			end = hole
		}
		if err := copyExtent(ctx, in, out, buf, data, end); err != nil {
			return err
		}
		off = end
	}
	return nil
}

func copyExtent(ctx context.Context, in, out *os.File, buf []byte, from, to int64) error {
	for pos := from; pos < to; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buf[:min(int64(len(buf)), to-pos)]
		n, err := in.ReadAt(chunk, pos)
		if n == 0 && err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			return nil // the file shrank under us; the sources are never written while copied
		}
		if err := writeNonZero(out, chunk[:n], pos); err != nil {
			return err
		}
		pos += int64(n)
		if chunkHook != nil {
			chunkHook()
		}
	}
	return nil
}

// writeNonZero writes b at off, skipping all-zero blocks so they stay holes.
func writeNonZero(out *os.File, b []byte, off int64) error {
	runStart := -1
	flush := func(end int) error {
		if runStart < 0 {
			return nil
		}
		_, err := out.WriteAt(b[runStart:end], off+int64(runStart))
		runStart = -1
		return err
	}
	for i := 0; i < len(b); i += blockSize {
		end := min(i+blockSize, len(b))
		if isZero(b[i:end]) {
			if err := flush(i); err != nil {
				return err
			}
			continue
		}
		if runStart < 0 {
			runStart = i
		}
	}
	return flush(len(b))
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
