//go:build unix

package dl

import "syscall"

// openNonblock keeps opening a FIFO from blocking until a writer shows up.
const openNonblock = syscall.O_NONBLOCK
