package sandboxd_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/sandboxd"
)

func TestGuestBootRoundTrip(t *testing.T) {
	ca := bytes.Repeat([]byte("-----CERT-----\n"), 3000) // ~45 KB, larger than MMDS' default limit
	in := sandboxd.GuestBoot{
		AgentID: "ag_1", Token: "secret", TunnelPort: 7443, Hostname: "agent-1", ResetIdentity: true,
		Env:   map[string]string{"HTTPS_PROXY": "http://{{server}}:3128", "PLAIN": "x"},
		Files: map[string][]byte{"/etc/agenthold/ca.pem": ca},
	}
	data, err := sandboxd.EncodeGuestBoot(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%sandboxd.GuestBootAlign != 0 || data[len(data)-1] != 0 {
		t.Fatalf("not padded: %d bytes", len(data))
	}
	if !bytes.HasPrefix(data, []byte(`{"version":1,`)) {
		t.Fatalf("prefix: %.40s", data)
	}
	out, err := sandboxd.DecodeGuestBoot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	in.Version = 1
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip:\n%+v\n%+v", in, out)
	}

	boot, env, err := out.Resolve(net.ParseIP("172.16.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if boot.TunnelURL != "ws://172.16.0.1:7443/tunnel" || boot.AgentID != "ag_1" || boot.Token != "secret" {
		t.Fatalf("boot = %+v", boot)
	}
	if env["HTTPS_PROXY"] != "http://172.16.0.1:3128" || env["PLAIN"] != "x" {
		t.Fatalf("env = %v", env)
	}
}

func TestGuestBootExplicitURL(t *testing.T) {
	b := sandboxd.GuestBoot{Version: 1, AgentID: "a", Token: "t", TunnelURL: "ws://{{server}}:1/tunnel"}
	boot, _, err := b.Resolve(net.ParseIP("10.0.0.1"))
	if err != nil || boot.TunnelURL != "ws://10.0.0.1:1/tunnel" {
		t.Fatalf("%+v %v", boot, err)
	}
	if _, _, err := (sandboxd.GuestBoot{AgentID: "a", Token: "t", TunnelPort: 1}).Resolve(nil); err == nil {
		t.Fatal("no gateway and no URL must fail")
	}
}

func TestGuestBootInvalid(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":         "",
		"trailing json": `{"version":1,"agentId":"a","token":"t","tunnelPort":1}{}`,
		"bad padding":   `{"version":1,"agentId":"a","token":"t","tunnelPort":1}` + "\x00garbage",
		"nul env":       `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"env":{"X":"a\u0000b"}}`,
		"zeros":         "\x00\x00\x00\x00",
		"not json":      "agent",
		"wrong version": `{"version":2,"agentId":"a","token":"t","tunnelPort":1}`,
		"no version":    `{"agentId":"a","token":"t","tunnelPort":1}`,
		"no token":      `{"version":1,"agentId":"a","tunnelPort":1}`,
		"no tunnel":     `{"version":1,"agentId":"a","token":"t"}`,
		"bad port":      `{"version":1,"agentId":"a","token":"t","tunnelPort":70000}`,
		"unknown field": `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"extra":true}`,
		"relative file": `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"files":{"etc/x":""}}`,
		"dotdot file":   `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"files":{"/etc/../x":""}}`,
		"proc file":     `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"files":{"/proc/sys/x":""}}`,
		"bad hostname":  `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"hostname":"a b"}`,
		"bad env":       `{"version":1,"agentId":"a","token":"t","tunnelPort":1,"env":{"A=B":"c"}}`,
	} {
		if _, err := sandboxd.DecodeGuestBoot(strings.NewReader(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := sandboxd.EncodeGuestBoot(sandboxd.GuestBoot{AgentID: "a"}); err == nil {
		t.Error("encode accepted an incomplete configuration")
	}
	// Zero padding is permitted; nonzero trailing data is rejected.
	good := `{"version":1,"agentId":"a","token":"t","tunnelPort":1}` + "\x00\x00"
	if _, err := sandboxd.DecodeGuestBoot(strings.NewReader(good)); err != nil {
		t.Errorf("padded: %v", err)
	}
}

func TestParseCmdline(t *testing.T) {
	args := sandboxd.ParseCmdline(`console=ttyS0 reboot=k panic=1 root=/dev/vda ro init=/opt/agenthold/sandboxd ` +
		`ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off agenthold.home=/dev/vde x="a b" root=/dev/vdz` + "\n")
	want := map[string]string{
		"console": "ttyS0", "reboot": "k", "panic": "1", "root": "/dev/vdz", "ro": "",
		"init": "/opt/agenthold/sandboxd", "ip": "172.16.0.2::172.16.0.1:255.255.255.252::eth0:off",
		"agenthold.home": "/dev/vde", "x": "a b",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got %v", args)
	}
	l := sandboxd.LayoutFromCmdline(args)
	if l != (sandboxd.Layout{Overlay: "/dev/vdb", Home: "/dev/vde", Config: "/dev/vdd"}) {
		t.Fatalf("layout %+v", l)
	}
}

func TestParseIP(t *testing.T) {
	c, err := sandboxd.ParseIP("172.16.0.2::172.16.0.1:255.255.255.252:myhost:eth0:off")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Client.Equal(net.ParseIP("172.16.0.2")) || !c.Gateway.Equal(net.ParseIP("172.16.0.1")) ||
		c.Netmask.String() != "fffffffc" || c.Hostname != "myhost" || c.Device != "eth0" || !c.Nameserver().Equal(c.Gateway) {
		t.Fatalf("%+v", c)
	}
	c, err = sandboxd.ParseIP("10.0.0.2::10.0.0.1:255.255.255.0::eth0:off:1.1.1.1:8.8.8.8")
	if err != nil || !c.Nameserver().Equal(net.ParseIP("1.1.1.1")) || len(c.DNS) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"", "dhcp", "10.0.0.2", "10.0.0.2::nope:255.255.255.0", "10.0.0.2::10.0.0.1:bad", "fd00::2::fd00::1"} {
		if _, err := sandboxd.ParseIP(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestClassifyDisk(t *testing.T) {
	blank := make([]byte, 1<<20)
	if k, _, err := sandboxd.ClassifyDisk(bytes.NewReader(blank)); err != nil || k != sandboxd.DiskBlank {
		t.Fatalf("blank: %v %v", k, err)
	}

	ext := make([]byte, 1<<20)
	sb := ext[1024:]
	binary.LittleEndian.PutUint16(sb[0x38:], 0xEF53)
	binary.LittleEndian.PutUint32(sb[0x18:], 2) // 4 KiB blocks
	binary.LittleEndian.PutUint32(sb[0x04:], 0x100)
	binary.LittleEndian.PutUint32(sb[0x60:], 0x80) // 64bit
	binary.LittleEndian.PutUint32(sb[0x150:], 1)
	binary.LittleEndian.PutUint16(sb[0x3A:], 1)
	copy(sb[0x78:], "agenthold-home")
	k, s, err := sandboxd.ClassifyDisk(bytes.NewReader(ext))
	if err != nil || k != sandboxd.DiskExt4 {
		t.Fatalf("ext4: %v %v", k, err)
	}
	if s.BlockSize != 4096 || s.BlockCount != 1<<32|0x100 || s.Label != "agenthold-home" || !s.CleanUnmount || s.Size() != int64(1<<32|0x100)*4096 {
		t.Fatalf("superblock %+v", s)
	}

	// Any data at all (here: a partition table signature) means "not ours to format".
	other := make([]byte, 1<<20)
	other[510], other[511] = 0x55, 0xAA
	if k, _, _ := sandboxd.ClassifyDisk(bytes.NewReader(other)); k != sandboxd.DiskOther {
		t.Fatalf("mbr: %v", k)
	}
	late := make([]byte, 1<<20)
	late[100<<10] = 1
	if k, _, _ := sandboxd.ClassifyDisk(bytes.NewReader(late)); k != sandboxd.DiskOther {
		t.Fatalf("data at 100 KiB: %v", k)
	}
	// Data beyond the probe window does not matter.
	far := make([]byte, 1<<20)
	far[512<<10] = 1
	if k, _, _ := sandboxd.ClassifyDisk(bytes.NewReader(far)); k != sandboxd.DiskBlank {
		t.Fatalf("data at 512 KiB: %v", k)
	}
	if _, _, err := sandboxd.ClassifyDisk(bytes.NewReader(make([]byte, 100))); err == nil {
		t.Fatal("tiny disk accepted")
	}
}

func TestMountPlan(t *testing.T) {
	early := sandboxd.EarlyMounts()
	if got := targets(early); !slices.Equal(got, []string{"/proc", "/sys", "/dev", "/run", sandboxd.InitRun}) {
		t.Fatalf("early: %v", got)
	}

	l := sandboxd.Layout{Overlay: "/dev/vdb", Home: "/dev/vdc", Config: "/dev/vdd"}
	plan := sandboxd.RootMounts(l)
	idx := func(pred func(sandboxd.Step) bool) int { return slices.IndexFunc(plan, pred) }
	disk := idx(func(s sandboxd.Step) bool { return s.Source == "/dev/vdb" })
	home := idx(func(s sandboxd.Step) bool { return s.Source == "/dev/vdc" })
	ovl := idx(func(s sandboxd.Step) bool { return s.FSType == "overlay" })
	ro := idx(func(s sandboxd.Step) bool {
		return s.Source == "/" && s.Flags&sandboxd.FlagRemount != 0 && s.Flags&sandboxd.FlagReadOnly != 0
	})
	moveDev := idx(func(s sandboxd.Step) bool { return s.Source == "/dev" && s.Flags&sandboxd.FlagMove != 0 })
	moveDisk := idx(func(s sandboxd.Step) bool { return s.Source == sandboxd.InitDisk && s.Flags&sandboxd.FlagMove != 0 })
	if disk < 0 || home < 0 || ovl < 0 || ro < 0 || moveDev < 0 || moveDisk < 0 {
		t.Fatalf("missing steps:\n%s", dump(plan))
	}
	if disk >= ovl || ro >= ovl || ovl >= home || home >= moveDisk || moveDisk >= moveDev {
		t.Fatalf("order:\n%s", dump(plan))
	}
	o := plan[ovl]
	if o.Target != sandboxd.NewRoot() || !strings.Contains(o.Data, "lowerdir=/run/agenthold/base") ||
		!strings.Contains(o.Data, "upperdir="+sandboxd.InitDisk+"/upper") || !strings.Contains(o.Data, "workdir="+sandboxd.InitDisk+"/work") {
		t.Fatalf("overlay step %s", o)
	}
	if plan[home].Target != sandboxd.NewRoot()+sandboxd.AgentHome || plan[home].FSType != "ext4" {
		t.Fatalf("home step %s", plan[home])
	}
	// Container runtimes' directories are bind mounts from the overlay disk's ext4.
	for _, d := range sandboxd.NativeDirs {
		i := idx(func(s sandboxd.Step) bool { return s.Target == sandboxd.NewRoot()+d && s.Flags&sandboxd.FlagBind != 0 })
		if i < 0 || !strings.HasPrefix(plan[i].Source, sandboxd.InitDisk+"/native/") || i < ovl {
			t.Fatalf("native dir %s:\n%s", d, dump(plan))
		}
	}
	// Every mount target exists before it is used (a mkdir step or a mount point of the base).
	made := map[string]bool{}
	for _, s := range append(early, plan...) {
		if s.IsMkdir() {
			made[s.Target] = true
			continue
		}
		switch s.Target {
		case "/proc", "/sys", "/dev", "/run", sandboxd.NewRoot() + "/run", sandboxd.NewRoot() + "/dev", sandboxd.NewRoot() + "/proc", sandboxd.NewRoot() + "/sys":
			continue // mount points in the base image
		}
		if !made[s.Target] {
			t.Errorf("mount on %s without a mkdir first", s.Target)
		}
	}

	late := sandboxd.LateMounts()
	for _, want := range []string{"/dev/pts", "/dev/shm", "/sys/fs/cgroup", "/tmp"} {
		if !slices.Contains(targets(late), want) {
			t.Errorf("late mounts lack %s", want)
		}
	}
	if !strings.Contains(sandboxd.HostsFile("box"), "127.0.1.1\tbox\n") {
		t.Error("hosts file")
	}
}

func TestParseEnvFile(t *testing.T) {
	env, err := sandboxd.ParseEnvFile(strings.NewReader("# c\n\nLANG=C.UTF-8\nPATH=/a:/b\nEMPTY=\nX=a=b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, map[string]string{"LANG": "C.UTF-8", "PATH": "/a:/b", "EMPTY": "", "X": "a=b"}) {
		t.Fatalf("%v", env)
	}
	if _, err := sandboxd.ParseEnvFile(strings.NewReader("NOEQUALS\n")); err == nil {
		t.Fatal("accepted a line without =")
	}
}

func targets(steps []sandboxd.Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Target)
	}
	return out
}

func dump(steps []sandboxd.Step) string {
	var b strings.Builder
	for i, s := range steps {
		b.WriteString(strings.Repeat(" ", 2) + string(rune('0'+i%10)) + " " + s.String() + "\n")
	}
	return b.String()
}

func TestEnsureIdentity(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(path string) bool {
		_, err := os.Lstat(filepath.Join(root, path))
		return err == nil
	}
	stamp := func() string {
		b, _ := os.ReadFile(filepath.Join(root, sandboxd.InstanceFile))
		return strings.TrimSpace(string(b))
	}
	write("etc/machine-id", "0123456789abcdef\n")
	write("var/lib/docker/engine-id", "engine")
	write("etc/ssh/ssh_host_ed25519_key", "key")
	write("etc/ssh/ssh_host_ed25519_key.pub", "pub")
	write("etc/ssh/sshd_config", "keep")
	write("home/agent/.ssh/id_ed25519", "the user's own key")

	// A system that was never stamped is not a clone: it is stamped, untouched.
	if reset, err := sandboxd.EnsureIdentity(root, "ag_source", false); err != nil || reset || stamp() != "ag_source" {
		t.Fatalf("first boot: reset=%v err=%v stamp=%q", reset, err, stamp())
	}
	if !exists("var/lib/docker/engine-id") || !exists("etc/ssh/ssh_host_ed25519_key") {
		t.Fatal("first boot cleared the identity")
	}
	// Booting again as the same agent changes nothing.
	if reset, err := sandboxd.EnsureIdentity(root, "ag_source", false); err != nil || reset {
		t.Fatalf("same agent: reset=%v err=%v", reset, err)
	}
	// The copy boots as another agent.
	if reset, err := sandboxd.EnsureIdentity(root, "ag_clone", false); err != nil || !reset {
		t.Fatalf("clone: reset=%v err=%v", reset, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/machine-id")); err != nil || len(b) != 0 {
		t.Fatalf("machine-id = %q, %v", b, err)
	}
	for _, gone := range []string{"var/lib/docker/engine-id", "etc/ssh/ssh_host_ed25519_key", "etc/ssh/ssh_host_ed25519_key.pub"} {
		if exists(gone) {
			t.Errorf("%s survived the reset", gone)
		}
	}
	for _, kept := range []string{"etc/ssh/sshd_config", "home/agent/.ssh/id_ed25519"} {
		if !exists(kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	if stamp() != "ag_clone" {
		t.Fatalf("stamp = %q", stamp())
	}
	if reset, err := sandboxd.EnsureIdentity(root, "ag_clone", false); err != nil || reset {
		t.Fatalf("second boot of the clone: reset=%v err=%v", reset, err)
	}
}

// The host's explicit instruction resets a clone of a system that never had a stamp.
func TestEnsureIdentityForced(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	must(os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte("abcd\n"), 0o644))
	reset, err := sandboxd.EnsureIdentity(root, "ag_clone", true)
	if err != nil || !reset {
		t.Fatalf("forced: reset=%v err=%v", reset, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/machine-id")); len(b) != 0 {
		t.Fatalf("machine-id = %q", b)
	}
	// It stamps, so the next boot (without the instruction) leaves the clone alone.
	must(os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte("its-own\n"), 0o644))
	if reset, err := sandboxd.EnsureIdentity(root, "ag_clone", false); err != nil || reset {
		t.Fatalf("second boot: reset=%v err=%v", reset, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/machine-id")); string(b) != "its-own\n" {
		t.Fatalf("machine-id = %q", b)
	}
}
