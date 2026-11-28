package sandboxd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPContent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ScreenshotDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(dir, "a.png")
	if err := os.WriteFile(shot, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := json.Marshal(map[string]any{"mcpContent": []any{
		map[string]any{"type": "text", "text": "saved"},
		map[string]any{"type": "image", "mimeType": "image/png", "file": shot},
		map[string]any{"type": "image", "mimeType": "image/png", "file": outside},
		map[string]any{"type": "image", "mimeType": "image/png", "file": filepath.Join(dir, "../../secret.txt")},
	}})
	blocks, ok := mcpContent(res, home)
	if !ok || len(blocks) != 2 {
		t.Fatalf("blocks %v", blocks)
	}
	if img := blocks[1].(map[string]any); img["data"] != "iVBORw==" || img["type"] != "image" {
		t.Fatalf("image %v", img)
	}
	for _, plain := range []string{`"text"`, `{"x":1}`, `[1]`, `{"mcpContent":[]}`} {
		if _, ok := mcpContent(json.RawMessage(plain), home); ok {
			t.Fatalf("%s taken as content", plain)
		}
	}
}
