//go:build !linux && !darwin

package sandboxd

// processExited cannot tell here; the harness counts as running.
func processExited(int) bool { return false }
