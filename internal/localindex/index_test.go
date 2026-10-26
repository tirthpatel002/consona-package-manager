package localindex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUniqueByNameAndPath(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)
	ca, _ := Canonical(a)
	cb, _ := Canonical(b)
	if _, err := s.Add(Entry{Name: "@u/a", Path: ca, DeviceID: "d", Registered: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	// Same folder twice (via a symlink and a relative-looking path).
	link := filepath.Join(root, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	cl, _ := Canonical(link + "/../a/.")
	if _, err := s.Add(Entry{Name: "@u/other", Path: cl}, false); !errors.As(err, &ce) || ce.Kind != "path" {
		t.Fatalf("same folder under another name must fail, got %v", err)
	}
	cl2, _ := Canonical(link)
	if cl2 != ca {
		t.Fatalf("symlink not resolved: %s vs %s", cl2, ca)
	}
	// Same name, other folder.
	if _, err := s.Add(Entry{Name: "@U/A", Path: cb}, false); !errors.As(err, &ce) || ce.Kind != "name" {
		t.Fatalf("same name must fail, got %v", err)
	}
	// Exact duplicate.
	if _, err := s.Add(Entry{Name: "@u/a", Path: ca}, false); !errors.As(err, &ce) || ce.Kind != "same" {
		t.Fatalf("duplicate must fail, got %v", err)
	}
	// --replace moves the name to b.
	removed, err := s.Add(Entry{Name: "@u/a", Path: cb}, true)
	if err != nil || len(removed) != 1 || removed[0].Path != ca {
		t.Fatalf("replace: %v %v", removed, err)
	}
	es, _ := s.List()
	if len(es) != 1 || es[0].Path != cb {
		t.Fatalf("after replace: %+v", es)
	}
	if !s.Scopes()["u"] {
		t.Fatal("scope u missing")
	}
	if _, err := s.Remove("@u/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove("@u/a"); err == nil {
		t.Fatal("second remove must fail")
	}
}

func TestNames(t *testing.T) {
	if n, err := DefaultName("@acme/Widget", "tirth"); err != nil || n != "@tirth/widget" {
		t.Fatalf("got %q %v", n, err)
	}
	if n, err := DefaultName("lib", "tirth"); err != nil || n != "@tirth/lib" {
		t.Fatalf("got %q %v", n, err)
	}
	for _, bad := range []string{"lib", "@/x", "@a/", "@a/b/c", "@A/b", "@a/.b"} {
		if _, _, err := SplitName(bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
