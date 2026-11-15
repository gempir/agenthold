package main

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit, err := systemdUnit("/opt/my app/agenthold", "/etc/agenthold/100%.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`ExecStart="/opt/my app/agenthold"`, `Environment="AGENTHOLD_CONFIG=/etc/agenthold/100%%.yaml"`, "KillMode=process", "Delegate=yes"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q: %s", want, unit)
		}
	}
	for _, path := range []string{"relative.yaml", "/etc/config\nExecStart=/bad", "/etc/config\x00"} {
		if _, err := systemdUnit("/usr/local/bin/agenthold", path); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
}
