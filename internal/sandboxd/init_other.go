//go:build !linux

package sandboxd

import (
	"errors"
	"fmt"
	"os"
)

// EnvInitChild marks the daemon process started by sandboxd's init mode.
const EnvInitChild = "AGENTHOLD_INIT_CHILD"

// RunInit is only available on Linux.
func RunInit(string) {
	fmt.Fprintln(os.Stderr, "sandboxd: init mode requires Linux")
	os.Exit(1)
}

// RequestShutdown is only available on Linux.
func RequestShutdown() error { return errors.New("shutdown requires Linux") }

// InInitChild reports whether this process is the daemon started by init.
func InInitChild() bool { return false }
