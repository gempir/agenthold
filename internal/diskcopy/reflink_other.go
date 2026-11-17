//go:build !linux

package diskcopy

import (
	"errors"
	"os"
)

func reflink(_, _ *os.File) error { return errors.ErrUnsupported }
