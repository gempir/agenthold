//go:build !linux

package sandboxd

import "errors"

// NetSetup is only available on Linux.
func NetSetup(string) error { return errors.New("net-setup requires Linux") }
