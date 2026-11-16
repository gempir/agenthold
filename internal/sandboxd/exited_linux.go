package sandboxd

import "golang.org/x/sys/unix"

// processExited reports whether the child pid has already exited, without
// reaping it (cmd.Wait still collects its status). A child that was reaped
// already counts as exited.
func processExited(pid int) bool {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	if err != nil {
		return err == unix.ECHILD
	}
	// WNOHANG with a still running child leaves the info zeroed.
	return info.Signo != 0
}
