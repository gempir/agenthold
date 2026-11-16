package sandboxd

import "golang.org/x/sys/unix"

// szomb is a zombie's p_stat (sys/proc.h).
const szomb = 5

// processExited reports whether the child pid has already exited (it is a
// zombie or gone), without reaping it.
func processExited(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return true
	}
	return kp.Proc.P_stat == szomb
}
