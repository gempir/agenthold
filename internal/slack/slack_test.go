package slack

import (
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"<@UBOT> hello": "hello",
		"see <https://example.com|the docs> &amp; <https://x.test>": "see [the docs](https://example.com) & https://x.test",
		"  <!here> ping  ": "@here ping",
		"a &lt;b&gt;":      "a <b>",
	} {
		if got := normalize(in, "UBOT"); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitPoint(t *testing.T) {
	text := strings.Repeat("word ", 1000) + "\n\n" + strings.Repeat("x", 2000)
	cut := splitPoint(text, 6000)
	if cut != 5002 || !strings.HasSuffix(text[:cut], "\n\n") {
		t.Fatalf("cut at %d", cut)
	}
	if got := splitPoint("short", 10); got != 5 {
		t.Fatalf("short text cut at %d", got)
	}
	// No break: a rune boundary.
	runes := strings.Repeat("ä", 100)
	if c := splitPoint(runes, 51); c != 50 {
		t.Fatalf("rune cut at %d", c)
	}
}

func TestChoiceLabel(t *testing.T) {
	opts := []gen.ModelOption{
		{Id: "effort", Kind: gen.Effort, Name: "Effort", Values: []gen.ModelOptionValue{{Value: "high", Name: "High"}}},
		{Id: "fast", Kind: gen.Toggle, Name: "Fast", Values: []gen.ModelOptionValue{{Value: "off"}, {Value: "on"}}},
	}
	cat := &gen.ModelCatalog{Models: []gen.CatalogModel{{Id: "m1", Name: "Model One", Options: &opts}}}
	if got := choiceLabel(cat, "m1", map[string]string{"fast": "on", "effort": "high"}, "x"); got != "Model One · High · Fast" {
		t.Fatalf("label %q", got)
	}
	if got := choiceLabel(cat, "", map[string]string{"fast": "off"}, "harness default"); got != "harness default" {
		t.Fatalf("label %q", got)
	}
}

func TestFooterAndManifest(t *testing.T) {
	if f := footer("end_turn", 75*time.Second); !strings.Contains(f, "Done · 1m 15s") {
		t.Fatalf("footer %q", f)
	}
	if f := footer("cancelled", 2*time.Second); !strings.Contains(f, "Cancelled · 2s") {
		t.Fatalf("footer %q", f)
	}
	s := &Service{o: Options{PublicURL: "https://agents.example.com"}}
	m := s.Manifest()
	if !strings.Contains(m["display_information"].(map[string]any)["description"].(string), "agents.example.com") {
		t.Fatalf("manifest %v", m)
	}
	settings := m["settings"].(map[string]any)
	if settings["socket_mode_enabled"] != true {
		t.Fatalf("settings %v", settings)
	}
}
