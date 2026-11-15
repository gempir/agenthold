//go:build !linux && !darwin

package runtimeassets

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return false }
