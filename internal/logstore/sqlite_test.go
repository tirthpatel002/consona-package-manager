//go:build !nosqlite

package logstore

import (
	"path/filepath"
	"testing"
)

// Schema migrations are versioned and re-opening is a no-op.
func TestSQLiteMigrations(t *testing.T) {
	if len(backends()) == 1 {
		t.Skip("built with nosqlite")
	}
	p := filepath.Join(t.TempDir(), "events.db")
	for i := 0; i < 2; i++ {
		s, err := openSQLite(p)
		if err != nil {
			t.Fatal(err)
		}
		var v int
		s.(*sqliteStore).db.QueryRow("PRAGMA user_version").Scan(&v)
		var mode string
		s.(*sqliteStore).db.QueryRow("PRAGMA journal_mode").Scan(&mode)
		if v != SchemaVersion || mode != "wal" {
			t.Fatalf("version %d mode %s", v, mode)
		}
		s.Close()
	}
}
