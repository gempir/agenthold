package main

import (
	"slices"
	"testing"
)

func TestRoleOf(t *testing.T) {
	for _, tc := range []struct {
		pid  int
		args []string
		want role
		rest []string
	}{
		{1, []string{"/init"}, roleInit, nil},
		{1, []string{"/opt/agenthold/sandboxd"}, roleInit, nil},
		// agenthold as PID 1 of a container is still the server.
		{1, []string{"/opt/agenthold/current/agenthold"}, roleServer, []string{}},
		{1, []string{"agenthold", "node"}, roleServer, []string{"node"}},
		{42, []string{"/init"}, roleServer, []string{}},
		{42, []string{"/opt/agenthold/sandboxd", "--boot", "/run/boot.json"}, roleSandboxd, []string{"--boot", "/run/boot.json"}},
		{42, []string{"/opt/agenthold/sandboxd", "mcp"}, roleSandboxd, []string{"mcp"}},
		{42, []string{"/usr/bin/agenthold", "sandboxd", "mcp"}, roleSandboxd, []string{"mcp"}},
		{42, []string{"/usr/bin/agenthold", "node", "sandboxd"}, roleServer, []string{"node", "sandboxd"}},
		{42, []string{"/usr/bin/agenthold"}, roleServer, []string{}},
		{42, nil, roleServer, nil},
	} {
		got, rest := roleOf(tc.pid, tc.args)
		if got != tc.want || !slices.Equal(rest, tc.rest) {
			t.Errorf("roleOf(%d, %q) = %v %q, want %v %q", tc.pid, tc.args, got, rest, tc.want, tc.rest)
		}
	}
}
