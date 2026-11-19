//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// dialSocket connects to a Unix socket the VMM created inside its jail. The
// jail is writable by the VMM user: a compromised Firecracker could replace
// the socket with a symlink to any socket of the host, which a plain dial (as
// root) would follow. The path is opened without following symlinks, checked
// to be a socket, and connected to through that descriptor.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open socket %s: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", fmt.Sprintf("/proc/self/fd/%d", fd))
}
