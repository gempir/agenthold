//go:build !linux && !darwin

package node

import (
	"context"
	"errors"
)

func hostCapacity(ctx context.Context, _ string) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	return Capacity{}, errors.New("node capacity reporting is unsupported on this operating system")
}
