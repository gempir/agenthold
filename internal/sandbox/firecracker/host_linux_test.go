//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeHost(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := hostRoot
	hostRoot = root
	t.Cleanup(func() { hostRoot = old })
}

func TestEnableHugePages(t *testing.T) {
	fakeHost(t, map[string]string{thpEnabled: "always [madvise] never\n"})
	prev, err := EnableHugePages()
	if err != nil || prev != "madvise" {
		t.Fatalf("EnableHugePages = %q, %v", prev, err)
	}
	got, _ := os.ReadFile(filepath.Join(hostRoot, thpEnabled))
	if string(got) != "always" {
		t.Fatalf("wrote %q", got)
	}
	fakeHost(t, map[string]string{thpEnabled: "[always] madvise never\n"})
	if prev, err = EnableHugePages(); err != nil || prev != "always" {
		t.Fatalf("already always: %q, %v", prev, err)
	}
}

func TestNestedVirtualization(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"apple vz", map[string]string{"sys/class/dmi/id/sys_vendor": "Apple Inc.\n", "sys/class/dmi/id/product_name": "Apple Virtualization Generic Platform\n"}, "Apple Inc. Apple Virtualization Generic Platform"},
		{"x86 guest", map[string]string{"proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu vme hypervisor lm\n", "sys/class/dmi/id/sys_vendor": "Hetzner\n"}, "Hetzner"},
		{"bare metal", map[string]string{"proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu vme lm\n", "sys/class/dmi/id/sys_vendor": "Supermicro\n"}, ""},
		{"nothing known", map[string]string{}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeHost(t, c.files)
			if got := NestedVirtualization(); got != c.want {
				t.Fatalf("NestedVirtualization = %q, want %q", got, c.want)
			}
		})
	}
}
