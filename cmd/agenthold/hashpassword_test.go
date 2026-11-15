package main

import (
	"os"
	"testing"

	"github.com/gempir/agenthold/internal/password"
)

func TestReadPasswordFromStdin(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"hunter2\n", "hunter2", true},
		{"no newline", "no newline", true},
		{"with spaces  \r\n", "with spaces  ", true},
		{"\n", "", false},
		{"", "", false},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(tc.in)
		_ = w.Close()
		got, err := readPassword(r, nil)
		_ = r.Close()
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("readPassword(%q) = %q, %v", tc.in, got, err)
		}
		if tc.ok {
			enc, _ := password.New(got)
			if h, err := password.Parse(enc); err != nil || !h.Verify(tc.want) {
				t.Errorf("hash of %q does not verify", got)
			}
		}
	}
}
