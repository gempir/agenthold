//go:build linux || darwin

package runtimeassets

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive advisory lock on path (creating it). ok is false when another
// process holds it. The lock ends with unlock, or when the process exits.
func tryLock(path string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = f.Close() }, true, nil
}
