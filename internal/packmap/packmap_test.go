package packmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: t.TempDir()}
}

func TestNormalize(t *testing.T) {
	good := []Entry{
		{Name: "@SC/Pkg", Repo: "acme/widgets", Path: "packages//pkg/"},
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: "a/b/c"},
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: "x"},
	}
	for i := range good {
		if err := Normalize(&good[i]); err != nil {
			t.Fatalf("Normalize(%+v): %v", good[i], err)
		}
	}
	if good[0].Name != "@sc/pkg" || good[0].Path != "packages/pkg" {
		t.Fatalf("not normalized: %+v", good[0])
	}
	bad := []Entry{
		{Name: "pkg", Repo: "acme/widgets", Path: "x"},        // no scope
		{Name: "@sc/pkg", Repo: "acme", Path: "x"},            // repo without owner
		{Name: "@sc/pkg", Repo: "a/b/c", Path: "x"},           // repo with extra slash
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: ""},     // empty path
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: "/"},    // root
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: ".."},   // parent
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: "../x"}, // escapes the repo
		{Name: "@sc/pkg", Repo: "acme/widgets", Path: "a/../../x"},
	}
	for i := range bad {
		if err := Normalize(&bad[i]); err == nil {
			t.Fatalf("Normalize(%+v) should fail", bad[i])
		}
	}
}

func TestAddLookupRemove(t *testing.T) {
	s := testStore(t)
	if _, ok, err := s.Lookup("@sc/miss"); err != nil || ok {
		t.Fatalf("empty lookup: %v %v", ok, err)
	}
	if err := s.Add(Entry{Name: "@sc/a", Repo: "acme/widgets", Path: "pkgs/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Entry{Name: "@sc/b", Repo: "acme/widgets", Path: "pkgs/b"}); err != nil {
		t.Fatal(err)
	}
	e, ok, err := s.Lookup("@SC/A")
	if err != nil || !ok || e.Path != "pkgs/a" || e.Mapped.IsZero() {
		t.Fatalf("lookup: %+v %v %v", e, ok, err)
	}
	// Add replaces the same name.
	if err := s.Add(Entry{Name: "@sc/a", Repo: "acme/other", Path: "x"}); err != nil {
		t.Fatal(err)
	}
	es, _ := s.List()
	if len(es) != 2 || es[0].Repo != "acme/other" {
		t.Fatalf("replace: %+v", es)
	}
	if err := s.Remove("@sc/a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("@sc/a"); err == nil {
		t.Fatal("removing an unmapped name should fail")
	}
	if got := s.Scopes(); len(got) != 1 || !got["sc"] {
		t.Fatalf("scopes: %v", got)
	}
}

func TestFileMode(t *testing.T) {
	s := testStore(t)
	if err := s.Add(Entry{Name: "@sc/a", Repo: "acme/widgets", Path: "pkgs/a"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(s.Dir, File))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %o", st.Mode().Perm())
	}
}

func TestSync(t *testing.T) {
	s := testStore(t)
	must := func(e Entry) {
		t.Helper()
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	must(Entry{Name: "@sc/old", Repo: "acme/widgets", Path: "pkgs/old", Synced: true})
	must(Entry{Name: "@sc/keep", Repo: "acme/widgets", Path: "pkgs/keep", Synced: true})
	must(Entry{Name: "@sc/manual", Repo: "acme/widgets", Path: "pkgs/manual"})
	must(Entry{Name: "@sc/else", Repo: "acme/other", Path: "x", Synced: true})
	declared, removed, err := s.Sync("acme/widgets", []Entry{
		{Name: "@sc/keep", Path: "pkgs/keep2"},
		{Name: "@sc/new", Path: "pkgs/new"},
	})
	if err != nil || declared != 2 || removed != 1 {
		t.Fatalf("sync: %d %d %v", declared, removed, err)
	}
	es, _ := s.List()
	byName := map[string]Entry{}
	for _, e := range es {
		byName[e.Name] = e
	}
	if len(es) != 4 {
		t.Fatalf("entries: %+v", es)
	}
	if e := byName["@sc/keep"]; !e.Synced || e.Path != "pkgs/keep2" {
		t.Fatalf("keep: %+v", e)
	}
	if e := byName["@sc/new"]; !e.Synced || e.Repo != "acme/widgets" {
		t.Fatalf("new: %+v", e)
	}
	if e := byName["@sc/manual"]; e.Synced || e.Path != "pkgs/manual" {
		t.Fatalf("manual must be untouched: %+v", e)
	}
	if e := byName["@sc/else"]; !e.Synced || e.Repo != "acme/other" {
		t.Fatalf("other repo must be untouched: %+v", e)
	}
	// A synced name overrides a manual entry for it.
	if _, _, err := s.Sync("acme/widgets", []Entry{{Name: "@sc/manual", Path: "pkgs/declared"}}); err != nil {
		t.Fatal(err)
	}
	e, _, _ := s.Lookup("@sc/manual")
	if !e.Synced || e.Repo != "acme/widgets" || e.Path != "pkgs/declared" {
		t.Fatalf("override: %+v", e)
	}
}

func TestSyncRejectsBad(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.Sync("acme/widgets", []Entry{{Name: "@sc/a", Path: "x"}, {Name: "@sc/a", Path: "y"}}); err == nil {
		t.Fatal("duplicate declaration must fail")
	}
	if _, _, err := s.Sync("acme/widgets", []Entry{{Name: "@sc/a", Path: ".."}}); err == nil {
		t.Fatal("bad path must fail")
	}
}

func TestParseMapFile(t *testing.T) {
	es, err := ParseMapFile([]byte(`{"packages":[{"name":"@acme/widgets-core","path":"packages/core"},{"name":"@acme/widgets-ui","path":"packages/ui"}]}`))
	if err != nil || len(es) != 2 || es[0].Name != "@acme/widgets-core" || es[1].Path != "packages/ui" || !es[0].Synced {
		t.Fatalf("parse: %+v %v", es, err)
	}
	for _, bs := range []string{
		`not json`,
		`{"packages":[]}`,
		`{"packages":[{"name":"unscoped","path":"x"}]}`,
		`{"packages":[{"name":"@a/b","path":".."}]}`,
	} {
		if _, err := ParseMapFile([]byte(bs)); err == nil {
			t.Fatalf("%q should fail", bs)
		}
	}
	// CRLF / whitespace tolerance is handled by the JSON decoder.
	if _, err := ParseMapFile([]byte("{\n  \"packages\": [ { \"name\": \"@a/b\", \"path\": \"p\" } ]\n}")); err != nil {
		t.Fatal(err)
	}
	_ = strings.TrimSpace
}
