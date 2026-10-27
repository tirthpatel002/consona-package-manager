package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A remapSource serves the same name and commit from changeable content,
// the situation a folder remap creates (see Revision.SourceID).
type remapSource struct {
	commit, sourceID, marker string
}

func (s *remapSource) Describe(r Ref) string { return "test/" + r.String() }
func (s *remapSource) Revisions(context.Context, Ref) ([]Revision, error) {
	return []Revision{{Version: "1.0.0", Tag: "v1.0.0", Commit: s.commit, SourceID: s.sourceID}}, nil
}
func (s *remapSource) Fetch(_ context.Context, _ Ref, _, dir string) error {
	return os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(s.marker), 0o644)
}

// markerBuilder packs the fetched tree's marker so a stale artifact is
// visible in the bytes.
type markerBuilder struct{}

func (markerBuilder) Ecosystem() string { return "npm" }
func (markerBuilder) Inspect(dir string) (*Info, error) {
	return &Info{Version: "1.0.0", Manifest: []byte(`{"name":"@o/x"}`), NeedsBuild: true}, nil
}
func (markerBuilder) Build(_ context.Context, dir string, r BuildRequest) (*Artifact, error) {
	bs, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.OutDir, "x.tgz"), bs, 0o644); err != nil {
		return nil, err
	}
	return &Artifact{Version: r.Version, File: "x.tgz", SHA1: "s", SHA512: "s"}, nil
}

func TestCacheID(t *testing.T) {
	rev := Revision{Commit: "abc123"}
	if got := cacheID(rev); got != "abc123" {
		t.Fatalf("no SourceID: %q", got)
	}
	a := cacheID(Revision{Commit: "abc123", SourceID: "ghfolder/acme/widgets/packages/a"})
	b := cacheID(Revision{Commit: "abc123", SourceID: "ghfolder/acme/widgets/packages/b"})
	if a == b {
		t.Fatal("different folders must not share a cache identity")
	}
	if a != cacheID(Revision{Commit: "abc123", SourceID: "ghfolder/acme/widgets/packages/a"}) {
		t.Fatal("cache identity must be stable")
	}
	for _, id := range []string{a, b} {
		if _, _, _, _, _, _, err := ValidateCacheKey(BuildCacheKey("npm", "o", "x", id, "1.0.0")); err != nil {
			t.Fatalf("cacheID must fit the cache-key alphabet: %v", err)
		}
	}
}

// Remapping a name to different content at the same commit and version
// must rebuild, never serve the previous mapping's cached bytes.
func TestRemapDoesNotServeStaleCache(t *testing.T) {
	cache := t.TempDir()
	src := &remapSource{commit: "abc123", sourceID: "ghfolder/acme/widgets/packages/a", marker: "content-A"}
	ref := Ref{Owner: "o", Name: "x"}
	ctx := context.Background()

	build := func() string {
		e := NewEngine(src, cache) // fresh engine: no memo, shared disk cache
		pkgs, err := e.Packages(ctx, markerBuilder{}, ref, "@o/x")
		if err != nil {
			t.Fatal(err)
		}
		bl, err := e.Build(ctx, markerBuilder{}, ref, "@o/x", pkgs[0])
		if err != nil {
			t.Fatal(err)
		}
		bs, err := os.ReadFile(bl.Path)
		if err != nil {
			t.Fatal(err)
		}
		return string(bs)
	}

	if got := build(); got != "content-A" {
		t.Fatalf("first build: %q", got)
	}
	// Same name, same commit, same version: only the mapping changed.
	src.sourceID, src.marker = "ghfolder/acme/widgets/packages/b", "content-B"
	if got := build(); got != "content-B" {
		t.Fatalf("after remap the cache served stale bytes: %q", got)
	}
	// Both cache entries coexist; the old one is not corrupted either.
	src.sourceID, src.marker = "ghfolder/acme/widgets/packages/a", "content-A"
	if got := build(); got != "content-A" {
		t.Fatalf("remap back: %q", got)
	}
	// SourceID travels into the remote-cache key too.
	key := BuildCacheKey("npm", "o", "x", cacheID(Revision{Commit: "abc123", SourceID: "ghfolder/acme/widgets/packages/a"}), "1.0.0")
	if key == BuildCacheKey("npm", "o", "x", "abc123", "1.0.0") {
		t.Fatal("remote cache key must include the source identity")
	}
}
