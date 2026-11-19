package api

import "time"

// SetOverviewTiming changes the overview's stats timeout and cache lifetime
// until the returned function is called. Tests using it must not run in parallel.
func SetOverviewTiming(timeout, ttl time.Duration) (restore func()) {
	t, l := overviewStatsTimeout, overviewStatsTTL
	overviewStatsTimeout, overviewStatsTTL = timeout, ttl
	return func() { overviewStatsTimeout, overviewStatsTTL = t, l }
}
