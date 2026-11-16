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
