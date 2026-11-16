//go:build !linux && !darwin

package runtimeassets

// tryLock is a no-op where advisory file locks are not available; release builds run on Linux.
func tryLock(string) (func(), bool, error) { return func() {}, true, nil }
