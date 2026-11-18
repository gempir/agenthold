//go:build linux

package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// serveResume accepts the host's resume messages on vsock port until ctx
// ends. Only the host can connect: a guest cannot reach its own vsock
// listener (the guest kernel has no vsock loopback transport).
func (d *Daemon) serveResume(ctx context.Context, port uint32) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		d.log.Warn("resume listener", "err", err)
		return
	}
	ln := os.NewFile(uintptr(fd), "vsock-resume")
	go func() { <-ctx.Done(); _ = unix.Shutdown(fd, unix.SHUT_RDWR); _ = ln.Close() }()
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		d.log.Warn("resume listener: bind", "err", err)
		return
	}
	if err := unix.Listen(fd, 4); err != nil {
		d.log.Warn("resume listener: listen", "err", err)
		return
	}
	for {
		nfd, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if ctx.Err() != nil {
			if err == nil {
				_ = unix.Close(nfd)
			}
			return
		}
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
			continue
		}
		if err != nil {
			d.log.Warn("resume listener: accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		d.serveResumeConn(nfd)
	}
}

func (d *Daemon) serveResumeConn(fd int) {
	tv := unix.NsecToTimeval((5 * time.Second).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
	f := os.NewFile(uintptr(fd), "vsock-resume-conn")
	defer f.Close()
	var reply ResumeReply
	m, err := readResume(f)
	if err != nil {
		reply.Error = err.Error()
	} else {
		reply = d.resume(m, setClock)
	}
	if reply.Error != "" {
		d.log.Warn("resume message refused", "err", reply.Error)
	}
	_ = json.NewEncoder(f).Encode(reply)
}

func setClock(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Settimeofday(&tv)
}

// syncAll flushes every filesystem to its disk.
func syncAll() { unix.Sync() }
