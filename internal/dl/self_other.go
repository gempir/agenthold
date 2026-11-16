//go:build !linux

package dl

import "os"

func openSelf() (*os.File, error) {
	p, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
