package docker

import (
	"net/netip"
	"testing"
)

func TestNextFree(t *testing.T) {
	pool := netip.MustParsePrefix("10.213.0.0/16")
	p := netip.MustParsePrefix
	tests := []struct {
		name string
		used []netip.Prefix
		want string
	}{
		{"empty", nil, "10.213.0.0/28"},
		{"first used", []netip.Prefix{p("10.213.0.0/28")}, "10.213.0.16/28"},
		{"gap", []netip.Prefix{p("10.213.0.0/28"), p("10.213.0.32/28")}, "10.213.0.16/28"},
		{"foreign overlapping network", []netip.Prefix{p("10.213.0.0/27"), p("172.17.0.0/16")}, "10.213.0.32/28"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextFree(pool, 28, tt.used)
			if err != nil || got.String() != tt.want {
				t.Fatalf("got %s, %v; want %s", got, err, tt.want)
			}
		})
	}
	if _, err := nextFree(p("10.0.0.0/28"), 28, []netip.Prefix{p("10.0.0.0/8")}); err == nil {
		t.Fatal("expected exhausted pool")
	}
}
