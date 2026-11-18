package sandboxd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadResume(t *testing.T) {
	ok := ResumeMessage{Version: ResumeVersion, AgentID: "ag_1", Token: "t", TimeUnixNano: time.Now().UnixNano()}
	for name, m := range map[string]ResumeMessage{
		"version":  {Version: ResumeVersion + 1, AgentID: "ag_1", Token: "t", TimeUnixNano: 1},
		"token":    {Version: ResumeVersion, AgentID: "ag_1", TimeUnixNano: 1},
		"time":     {Version: ResumeVersion, AgentID: "ag_1", Token: "t"},
		"file":     {Version: ResumeVersion, AgentID: "ag_1", Token: "t", TimeUnixNano: 1, Files: map[string][]byte{"/proc/x": nil}},
		"relative": {Version: ResumeVersion, AgentID: "ag_1", Token: "t", TimeUnixNano: 1, Files: map[string][]byte{"etc/x": nil}},
	} {
		data, _ := json.Marshal(m)
		if _, err := readResume(bytes.NewReader(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	data, _ := json.Marshal(ok)
	if m, err := readResume(bytes.NewReader(data)); err != nil || m.Token != "t" {
		t.Fatal(m, err)
	}
}

// A resume steps the clock to the host's, writes the boot files, keeps the
// new token for a restarted daemon and makes the reconnect loop redial.
func TestResumeAppliesWake(t *testing.T) {
	dir := t.TempDir()
	bootPath := filepath.Join(dir, "boot.json")
	d := &Daemon{cfg: Config{Boot: Boot{AgentID: "ag_1", Token: "old", TunnelURL: "ws://gw/tunnel"}, BootPath: bootPath}, log: slog.New(slog.DiscardHandler), kick: make(chan struct{}, 1)}
	d.boot = d.cfg.Boot
	redialed := false
	d.redial = func() { redialed = true }
	host := time.Now().Add(time.Hour)
	var stepped time.Time
	file := filepath.Join(dir, "etc", "f")
	reply := d.resume(ResumeMessage{Version: ResumeVersion, AgentID: "ag_1", Token: "new", TimeUnixNano: host.UnixNano(), Files: map[string][]byte{file: []byte("x")}},
		func(t time.Time) error { stepped = t; return nil })
	if !reply.OK || reply.ClockStepMillis < 59*60*1000 {
		t.Fatalf("reply %+v", reply)
	}
	if !stepped.Equal(time.Unix(0, host.UnixNano())) {
		t.Fatalf("clock stepped to %s", stepped)
	}
	if got, _ := os.ReadFile(file); string(got) != "x" {
		t.Fatalf("boot file %q", got)
	}
	var b Boot
	data, _ := os.ReadFile(bootPath)
	if err := json.Unmarshal(data, &b); err != nil || b.Token != "new" || b.TunnelURL != "ws://gw/tunnel" {
		t.Fatalf("boot file %s %v", data, err)
	}
	if d.boot.Token != "new" || !redialed || len(d.kick) != 1 {
		t.Fatalf("token %q redialed %v kick %d", d.boot.Token, redialed, len(d.kick))
	}
	if r := d.resume(ResumeMessage{Version: ResumeVersion, AgentID: "ag_2", Token: "x", TimeUnixNano: 1}, func(time.Time) error { return nil }); r.OK {
		t.Fatal("resume for another machine accepted")
	}
}
