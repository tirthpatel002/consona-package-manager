package npm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

const (
	recCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// tagSource serves a mutable tag list (tests move tags and remap sources)
// and records the commits it is asked to fetch.
type tagSource struct {
	tags     []core.Tag
	sourceID string
	fetched  []string
}

func (s *tagSource) Describe(ref core.Ref) string { return "github.com/" + ref.String() }
func (s *tagSource) Revisions(_ context.Context, ref core.Ref) ([]core.Revision, error) {
	if ref != (core.Ref{Owner: "o", Name: "lib"}) {
		return nil, core.ErrNotFound
	}
	revs := core.RevisionsFromTags(s.tags, "")
	for i := range revs {
		revs[i].SourceID = s.sourceID
	}
	return revs, nil
}
func (s *tagSource) Fetch(_ context.Context, _ core.Ref, commit, dir string) error {
	s.fetched = append(s.fetched, commit)
	return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@o/lib","version":"1.0.0"}`), 0o644)
}

// commitBuilder packs bytes that name the commit, so a test can tell which
// revision was served.
type commitBuilder struct{}

func (commitBuilder) Ecosystem() string { return "npm" }
func (commitBuilder) Inspect(string) (*core.Info, error) {
	return &core.Info{Version: "1.0.0", Manifest: []byte(`{"name":"@o/lib"}`)}, nil
}
func (commitBuilder) Build(_ context.Context, _ string, r core.BuildRequest) (*core.Artifact, error) {
	data := []byte("tarball-" + r.Revision.Commit + r.Revision.SourceID)
	file := npmpack.FileName(r.Name, r.Version)
	if err := os.WriteFile(filepath.Join(r.OutDir, file), data, 0o644); err != nil {
		return nil, err
	}
	s1, s512 := npmpack.Hashes(data)
	return &core.Artifact{Version: r.Version, File: file, Manifest: []byte(`{"name":"@o/lib"}`), SHA1: s1, SHA512: s512}, nil
}

func newRecoveryServer(t *testing.T, src *tagSource) *httptest.Server {
	t.Helper()
	routeScope(t, "o")
	e := core.NewEngine(src, t.TempDir())
	e.TTL = -1 // always re-resolve: the production memo would mask a tag move
	h := &handler{e: e, b: commitBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func packumentTarball(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/@o%2flib")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("packument: %v %v", err, resp.Status)
	}
	var doc struct {
		Versions map[string]struct {
			Dist struct{ Tarball string }
		}
	}
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	v, ok := doc.Versions["1.0.0"]
	if !ok {
		t.Fatalf("no 1.0.0 in packument")
	}
	return v.Dist.Tarball
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(bs)
}

func TestMovedTagServesPinnedCommit(t *testing.T) {
	src := &tagSource{tags: []core.Tag{{Name: "v1.0.0", Commit: recCommitA}}}
	srv := newRecoveryServer(t, src)

	before := packumentTarball(t, srv)
	if !strings.Contains(before, recCommitA) {
		t.Fatalf("tarball URL must pin the commit: %q", before)
	}
	code, body := getBody(t, before)
	if code != 200 || body != "tarball-"+recCommitA {
		t.Fatalf("first fetch: %d %q", code, body)
	}

	// The tag moves: new installs get the new commit, the old lockfile's
	// URL keeps serving the pinned one.
	src.tags = []core.Tag{{Name: "v1.0.0", Commit: recCommitB}}
	after := packumentTarball(t, srv)
	if !strings.Contains(after, recCommitB) || after == before {
		t.Fatalf("moved tag must change the tarball URL: %q -> %q", before, after)
	}
	src.fetched = nil
	code, body = getBody(t, before)
	if code != 200 || body != "tarball-"+recCommitA {
		t.Fatalf("old lockfile URL must serve the pinned commit: %d %q", code, body)
	}
	for _, f := range src.fetched {
		if f == recCommitA {
			t.Fatalf("the pinned build must come from the cache, not a re-fetch: %v", src.fetched)
		}
	}

	// A legacy (version-only) URL keeps the old behavior: the version
	// re-resolves, so a moved tag is detected by integrity, not hidden.
	code, body = getBody(t, srv.URL+"/@o/lib/-/o-lib-1.0.0.tgz")
	if code != 200 || body != "tarball-"+recCommitB {
		t.Fatalf("legacy URL must serve the current tag target: %d %q", code, body)
	}
}

func TestRemapNeverServesNewFolderUnderOldURL(t *testing.T) {
	src := &tagSource{
		tags:     []core.Tag{{Name: "v1.0.0", Commit: recCommitA}},
		sourceID: "ghfolder/o/lib/packages/a",
	}
	srv := newRecoveryServer(t, src)

	before := packumentTarball(t, srv)
	if !strings.Contains(before, recCommitA+"-src") {
		t.Fatalf("mapped source must bind its identity into the URL: %q", before)
	}
	wantA := "tarball-" + recCommitA + "ghfolder/o/lib/packages/a"
	code, body := getBody(t, before)
	if code != 200 || body != wantA {
		t.Fatalf("first fetch: %d %q", code, body)
	}

	// Remapped (same name, same commit, another folder): the old URL
	// still serves the ORIGINAL bytes from the cache - never a rebuild
	// from the new mapping under the old identity.
	src.sourceID = "ghfolder/o/lib/packages/b"
	code, body = getBody(t, before)
	if code != 200 || body != wantA {
		t.Fatalf("old URL after remap must serve the original bytes: %d %q", code, body)
	}

	// On a cold cache the old URL fails honestly instead of serving the
	// new mapping's bytes.
	u, _ := url.Parse(before)
	srv2 := newRecoveryServer(t, src)
	code, _ = getBody(t, srv2.URL+u.Path)
	if code != 404 {
		t.Fatalf("cold-cache old URL after remap must be 404, got %d", code)
	}
}
