package local

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (*Source, string, core.Ref) {
	t.Helper()
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	write(t, filepath.Join(pkg, "package.json"), `{"name":"@me/lib","version":"1.2.0"}`)
	write(t, filepath.Join(pkg, "index.js"), "module.exports = 1\n")
	idx := &localindex.Store{Dir: filepath.Join(dir, "cfg")}
	if _, err := idx.Add(localindex.Entry{Name: "@me/lib", Path: pkg, DeviceID: "d1"}, false); err != nil {
		t.Fatal(err)
	}
	return New(idx, filepath.Join(dir, "home")), pkg, core.Ref{Owner: "me", Name: "lib"}
}

func rev(t *testing.T, s *Source, ref core.Ref) core.Revision {
	t.Helper()
	revs, err := s.Revisions(context.Background(), ref)
	if err != nil || len(revs) != 1 || !revs[0].Head {
		t.Fatalf("revisions: %v %+v", err, revs)
	}
	return revs[0]
}

var verRe = regexp.MustCompile(`^1\.2\.0-local\.(\d+)\.h[0-9a-f]{8}$`)

func TestContentHashVersioning(t *testing.T) {
	s, pkg, ref := setup(t)
	r1 := rev(t, s, ref)
	if m := verRe.FindStringSubmatch(r1.Version); m == nil || m[1] != "1" || !core.ValidVersion(r1.Version) {
		t.Fatalf("version %q", r1.Version)
	}
	if !strings.HasPrefix(r1.Commit, CommitPrefix) {
		t.Fatal(r1.Commit)
	}
	// Unchanged content: same version and commit (stable lockfiles).
	if r := rev(t, s, ref); r != r1 {
		t.Fatalf("unchanged folder changed revision: %+v vs %+v", r, r1)
	}
	// node_modules, .git and OS junk do not count.
	write(t, filepath.Join(pkg, "node_modules", "x", "index.js"), "junk")
	write(t, filepath.Join(pkg, ".git", "HEAD"), "ref")
	write(t, filepath.Join(pkg, ".DS_Store"), "junk")
	if r := rev(t, s, ref); r != r1 {
		t.Fatalf("ignored files changed revision: %+v", r)
	}
	// Changing a file (same size, new mtime) invalidates.
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(pkg, "index.js"), "module.exports = 2\n")
	r2 := rev(t, s, ref)
	if r2.Commit == r1.Commit || r2.Version == r1.Version || core.CompareVersions(r2.Version, r1.Version) <= 0 {
		t.Fatalf("content change: %+v -> %+v", r1, r2)
	}
	if m := verRe.FindStringSubmatch(r2.Version); m == nil || m[1] != "2" {
		t.Fatalf("version %q", r2.Version)
	}
	// Adding a file invalidates too; a fresh Source (new process) agrees.
	write(t, filepath.Join(pkg, "lib", "extra.js"), "x")
	r3 := rev(t, s, ref)
	s2 := New(s.Index, filepath.Dir(s.StateDir))
	if r := rev(t, s2, ref); r != r3 || r3.Commit == r2.Commit {
		t.Fatalf("fresh source: %+v vs %+v", r, r3)
	}
	// Reverting content is still a new, higher version (npm caches by version).
	os.Remove(filepath.Join(pkg, "lib", "extra.js"))
	r4 := rev(t, s, ref)
	if core.CompareVersions(r4.Version, r3.Version) <= 0 {
		t.Fatalf("revert: %s !> %s", r4.Version, r3.Version)
	}
	// A package.json version bump changes the base.
	write(t, filepath.Join(pkg, "package.json"), `{"name":"@me/lib","version":"1.3.0"}`)
	if r := rev(t, s, ref); !strings.HasPrefix(r.Version, "1.3.0-local.") {
		t.Fatal(r.Version)
	}
}

func TestFetchCopiesWithoutTouchingFolder(t *testing.T) {
	s, pkg, ref := setup(t)
	write(t, filepath.Join(pkg, "node_modules", "dep", "index.js"), "x")
	write(t, filepath.Join(pkg, ".git", "config"), "x")
	if err := os.Symlink("index.js", filepath.Join(pkg, "link.js")); err != nil {
		t.Fatal(err)
	}
	before, _ := HashTree(pkg)
	r := rev(t, s, ref)
	out := t.TempDir()
	if err := s.Fetch(context.Background(), ref, r.Commit, out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"node_modules", ".git", "link.js"} {
		if _, err := os.Lstat(filepath.Join(out, p)); err == nil {
			t.Fatalf("%s copied", p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(out, "index.js")); err != nil || string(b) != "module.exports = 1\n" {
		t.Fatal(err)
	}
	after, _ := HashTree(pkg)
	if before != after {
		t.Fatal("folder modified")
	}
	if _, err := os.Stat(filepath.Join(pkg, "package-lock.json")); err == nil {
		t.Fatal("lockfile written into folder")
	}
	// A stale commit (folder changed since resolve) is refused.
	if err := s.Fetch(context.Background(), ref, CommitPrefix+strings.Repeat("0", 32), t.TempDir()); err == nil {
		t.Fatal("stale commit fetched")
	}
}

func TestNotRegistered(t *testing.T) {
	s, _, _ := setup(t)
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "me", Name: "other"})
	if !IsNotRegistered(err) {
		t.Fatal(err)
	}
	if s.Live(core.Ref{Owner: "me", Name: "other"}) || !s.Live(core.Ref{Owner: "me", Name: "lib"}) {
		t.Fatal("live")
	}
}

func TestMakeVersion(t *testing.T) {
	h := "0123456789abcdef"
	for in, want := range map[string]string{
		"1.0.0":          "1.0.0-local.3.h01234567",
		"v2.1.0+build.5": "2.1.0-local.3.h01234567",
		"1.0.0-beta.2":   "1.0.0-beta.2.local.3.h01234567",
		"":               "0.0.0-local.3.h01234567",
		"nonsense":       "0.0.0-local.3.h01234567",
	} {
		if got := MakeVersion(in, 3, h); got != want || !core.ValidVersion(got) {
			t.Errorf("MakeVersion(%q) = %q, want %q", in, got, want)
		}
	}
	// "h" keeps an all-digit hash prefix from being a numeric identifier with a leading zero.
	if v := MakeVersion("1.0.0", 1, "0123456789"); !core.ValidVersion(v) {
		t.Fatal(v)
	}
}
