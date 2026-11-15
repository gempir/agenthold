//go:build darwin

package node

import (
	"strings"
	"testing"
)

func TestParseVMStat(t *testing.T) {
	tests := []struct {
		name, input string
		want        uint64
		invalid     bool
	}{
		{"normal", "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 5.\nPages inactive: 10.\nPages speculative: 2.\n", 17 * 16384, false},
		{"missing page size", "header\nPages free: 5.\n", 0, true},
		{"missing count", "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 5.\n", 0, true},
		{"invalid count", "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: nope.\n", 0, true},
		{"overflow", "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 18446744073709551615.\nPages inactive: 1.\nPages speculative: 1.\n", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVMStat(strings.NewReader(tc.input))
			if (err != nil) != tc.invalid || (!tc.invalid && got != tc.want) {
				t.Fatalf("got %d, %v; want %d invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}
