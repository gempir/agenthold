package main

import (
	"path/filepath"
	"testing"

	"github.com/gempir/agenthold/internal/config"
)

func TestSQLiteBackupPath(t *testing.T) {
	for _, location := range []string{"postgres://localhost/agents", "sqlite::memory:"} {
		if _, err := sqliteBackupPath(&config.Config{Database: config.Database{URL: location}}); err == nil {
			t.Errorf("accepted unsupported backup location %q", location)
		}
	}
	want := filepath.Join(t.TempDir(), "custom database.db")
	got, err := sqliteBackupPath(&config.Config{Database: config.Database{URL: "sqlite:" + want}})
	if err != nil || got != want {
		t.Fatalf("custom restore destination: %q, %v", got, err)
	}
}
