package api_test

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

var goroutineHead = regexp.MustCompile(`^goroutine (\d+) \[`)

// goroutines returns the stacks of all goroutines by id.
func goroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := map[string]string{}
	for _, g := range strings.Split(string(buf), "\n\n") {
		if m := goroutineHead.FindStringSubmatch(g); m != nil {
			out[m[1]] = g
		}
	}
	return out
}

// noGoroutinesLeft fails t if a goroutine started by this test's environment
// (from the code under test) is still running once every cleanup registered
// after it has run. It must be registered right after the environment's first
// t.TempDir, so that it runs after the other cleanups and before the
// directories are removed: whatever still runs then may write into them.
func noGoroutinesLeft(t *testing.T, before map[string]string) {
	t.Helper()
	t.Cleanup(func() {
		var left []string
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			left = left[:0]
			for id, g := range goroutines() {
				if _, old := before[id]; old || !strings.Contains(g, "gempir/agenthold/") || strings.Contains(g, "noGoroutinesLeft") {
					continue
				}
				left = append(left, g)
			}
			if len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		for _, g := range left {
			t.Errorf("goroutine still running after the test environment was torn down (it may write into the test's directories):\n%s", fmt.Sprint(g))
		}
	})
}
