package sandboxd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// DiskKind is what a block device holds.
type DiskKind int

// Disk kinds.
const (
	DiskBlank DiskKind = iota // all zeros where a filesystem would be: format it
	DiskExt4                  // an ext2/3/4 filesystem
	DiskOther                 // something else: never format
)

func (k DiskKind) String() string {
	switch k {
	case DiskBlank:
		return "blank"
	case DiskExt4:
		return "ext4"
	default:
		return "unknown"
	}
}

// Superblock is the part of an ext4 superblock sandboxd cares about.
type Superblock struct {
	BlockSize    int64
	BlockCount   uint64
	Label        string
	CleanUnmount bool
}

// Size is the filesystem size in bytes.
func (s Superblock) Size() int64 { return int64(s.BlockCount) * s.BlockSize }

// probeSize is how much of a disk ClassifyDisk reads: the superblock, and the
// area other filesystems and partition tables put their signatures in.
const probeSize = 128 << 10

// ClassifyDisk looks at the start of a disk. A disk counts as blank only if
// its first 128 KiB are all zero (a freshly truncated sparse file), so a disk
// with any other content is never formatted over.
func ClassifyDisk(r io.ReaderAt) (DiskKind, Superblock, error) {
	var sb Superblock
	buf := make([]byte, probeSize)
	n, err := r.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return DiskOther, sb, err
	}
	buf = buf[:n]
	if n < 2048 {
		return DiskOther, sb, fmt.Errorf("disk is too small (%d bytes)", n)
	}
	s := buf[1024:2048]
	if binary.LittleEndian.Uint16(s[0x38:]) == 0xEF53 {
		logBlock := binary.LittleEndian.Uint32(s[0x18:])
		if logBlock > 6 {
			return DiskOther, sb, fmt.Errorf("ext4 superblock: bad block size 2^%d KiB", logBlock)
		}
		sb.BlockSize = 1024 << logBlock
		sb.BlockCount = uint64(binary.LittleEndian.Uint32(s[0x04:]))
		const incompat64bit = 0x80
		if binary.LittleEndian.Uint32(s[0x60:])&incompat64bit != 0 {
			sb.BlockCount |= uint64(binary.LittleEndian.Uint32(s[0x150:])) << 32
		}
		label := s[0x78 : 0x78+16]
		for i, c := range label {
			if c == 0 {
				label = label[:i]
				break
			}
		}
		sb.Label = string(label)
		sb.CleanUnmount = binary.LittleEndian.Uint16(s[0x3A:])&1 != 0
		return DiskExt4, sb, nil
	}
	for _, c := range buf {
		if c != 0 {
			return DiskOther, sb, nil
		}
	}
	return DiskBlank, sb, nil
}
