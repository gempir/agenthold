package store

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthold.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkspace(t.Context(), dbCreateWorkspace("ws_1")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws, err := s.GetWorkspace(t.Context(), "ws_1")
	if err != nil || ws.Name != "test" {
		t.Fatalf("got %+v, %v", ws, err)
	}
}
