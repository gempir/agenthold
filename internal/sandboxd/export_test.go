package sandboxd

import "time"

// SetServiceTiming shortens supervision timing for tests and returns a restore function.
func SetServiceTiming(minBackoff, maxBackoff, healthy, grace time.Duration) func() {
	a, b, c, d := serviceMinBackoff, serviceMaxBackoff, serviceHealthy, serviceStopGrace
	serviceMinBackoff, serviceMaxBackoff, serviceHealthy, serviceStopGrace = minBackoff, maxBackoff, healthy, grace
	return func() { serviceMinBackoff, serviceMaxBackoff, serviceHealthy, serviceStopGrace = a, b, c, d }
}

// SetServiceLogMax changes the rotation size for tests and returns a restore function.
func SetServiceLogMax(n int64) func() {
	old := serviceLogMax
	serviceLogMax = n
	return func() { serviceLogMax = old }
}

// SetHarnessTestHooks orders the harness bridge's steps in tests.
func SetHarnessTestHooks(beforeWait func(pid int), afterStop func()) {
	testHooks.beforeWait, testHooks.afterStop = beforeWait, afterStop
}

// ProcessExited exposes processExited.
var ProcessExited = processExited

// SetBrowserTiming changes how long input settles and when an unused browser
// stops, and returns a restore function.
func SetBrowserTiming(settle, idle time.Duration) func() {
	a, b := browserSettle, browserIdle
	browserSettle, browserIdle = settle, idle
	return func() { browserSettle, browserIdle = a, b }
}
