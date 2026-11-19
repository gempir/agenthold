package sandboxd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

func TestReadResume(t *testing.T) {
	ok := ResumeMessage{Version: ResumeVersion, AgentID: "ag_1", Token: "t", TimeUnixNano: time.Now().UnixNano()}
	for name, m := range map[string]ResumeMessage{
		"version":  {Version: ResumeVersion + 1, AgentID: "ag_1", Token: "t", TimeUnixNano: 1},
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
	// An empty token (resume of a VM an interrupted sleep left paused)
	// keeps the current one, and still redials.
	redialed = false
	if r := d.resume(ResumeMessage{Version: ResumeVersion, AgentID: "ag_1", TimeUnixNano: host.UnixNano()}, func(time.Time) error { return nil }); !r.OK || d.boot.Token != "new" || !redialed {
		t.Fatalf("empty token: %+v token %q redialed %v", r, d.boot.Token, redialed)
	}
	if r := d.resume(ResumeMessage{Version: ResumeVersion, AgentID: "ag_2", Token: "x", TimeUnixNano: 1}, func(time.Time) error { return nil }); r.OK {
		t.Fatal("resume for another machine accepted")
	}
}

// Services changed while the machine slept arrive with the resume: they are
// applied, and the file keeps the delivered set (a later runtime change saves
// on top of it, not on top of the set from before the sleep).
func TestResumeAppliesServices(t *testing.T) {
	dir := t.TempDir()
	svcFile := filepath.Join(dir, "services.json")
	old, _ := json.Marshal([]tunnel.ServiceSpec{{Name: "old", Command: "exec sleep 30", Enabled: true}})
	if err := os.WriteFile(svcFile, old, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{Boot: Boot{AgentID: "ag_1", Token: "t", TunnelURL: "ws://gw/tunnel"}, Home: filepath.Join(dir, "home"), ServicesFile: svcFile, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := loadServices(svcFile)
	if err != nil {
		t.Fatal(err)
	}
	d.services.start(t.Context(), specs)
	t.Cleanup(d.services.stopAll)
	delivered, _ := json.Marshal([]tunnel.ServiceSpec{{Name: "web", Command: "exec sleep 30", Enabled: true}})
	reply := d.resume(ResumeMessage{Version: ResumeVersion, AgentID: "ag_1", Token: "new", TimeUnixNano: time.Now().UnixNano(), Files: map[string][]byte{svcFile: delivered}},
		func(time.Time) error { return nil })
	if !reply.OK {
		t.Fatal(reply.Error)
	}
	names := func() []string {
		var out []string
		for _, st := range d.services.status() {
			out = append(out, st.Name+"="+st.State)
		}
		return out
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := names()
		if len(got) == 1 && got[0] == "web="+tunnel.ServiceRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("services after resume: %v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, _ := os.ReadFile(svcFile); !bytes.Equal(b, delivered) {
		t.Fatalf("services file after resume: %s", b)
	}
	d.services.save()
	if specs, err := loadServices(svcFile); err != nil || len(specs) != 1 || specs[0].Name != "web" {
		t.Fatalf("saved after resume: %+v %v", specs, err)
	}
}
