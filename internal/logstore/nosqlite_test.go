//go:build nosqlite

package logstore

import (
	"strings"
	"testing"
)

// Built without SQLite: Open falls back to JSON lines and says why.
func TestNoSQLiteBuildFallsBack(t *testing.T) {
	t.Setenv("CONSONA_LOG_BACKEND", "")
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, _ := s.Info()
	if in.Backend != "jsonl" || !strings.Contains(in.Fallback, "nosqlite") {
		t.Fatalf("%+v", in)
	}
}
