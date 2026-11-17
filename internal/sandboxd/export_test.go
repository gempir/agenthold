package sandboxd

// SetHarnessTestHooks orders the harness bridge's steps in tests.
func SetHarnessTestHooks(beforeWait func(pid int), afterStop func()) {
	testHooks.beforeWait, testHooks.afterStop = beforeWait, afterStop
}

// ProcessExited exposes processExited.
var ProcessExited = processExited
