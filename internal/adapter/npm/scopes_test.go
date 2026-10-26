package npm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// liveSource is a local-folder-like source whose single version changes.
type liveSource struct {
	mu  sync.Mutex
	ver string
}

func (s *liveSource) Describe(r core.Ref) string { return "folder/" + r.String() }
func (s *liveSource) Revisions(_ context.Context, r core.Ref) ([]core.Revision, error) {
	if r != (core.Ref{Owner: "me", Name: "lib"}) {
		return nil, core.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return []core.Revision{{Version: s.ver, Commit: "local-" + s.ver, Head: true}}, nil
}
func (s *liveSource) Fetch(_ context.Context, _ core.Ref, _, dir string) error {
	return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@me/lib"}`), 0o644)
}
func (s *liveSource) Live(core.Ref) bool { return true }

// Scopes from ExtraScopes (registered folders, remotes) are private even
// when the npmrc does not route them, and a tarball of an earlier live
// version (still in a lockfile) is served from the cache.
func TestExtraScopesAndOldLiveTarball(t *testing.T) {
	routeScope(t, "o")
	ExtraScopes = func() map[string]bool { return map[string]bool{"me": true} }
	defer func() { ExtraScopes = nil }()
	src := &liveSource{ver: "1.0.0-local.1.haaaaaaaa"}
	h := &handler{e: core.NewEngine(src, t.TempDir()), b: fakeBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	defer srv.Close()
	get := func(p string) (int, string) {
		resp, err := noRedirect.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, _ := get("/@me%2flib"); c != 200 {
		t.Fatalf("extra scope not served: %d", c)
	}
	old := "/@me/lib/-/me-lib-1.0.0-local.1.haaaaaaaa.tgz"
	if c, b := get(old); c != 200 || b != "tarball-bytes" {
		t.Fatalf("tarball: %d %q", c, b)
	}
	src.mu.Lock()
	src.ver = "1.0.0-local.2.hbbbbbbbb"
	src.mu.Unlock()
	if c, b := get("/@me%2flib"); c != 200 || !contains(b, "1.0.0-local.2.hbbbbbbbb") || contains(b, "local.1") {
		t.Fatalf("live packument not refreshed: %d %s", c, b)
	}
	if c, _ := get(old); c != 200 {
		t.Fatalf("old cached tarball: %d", c)
	}
	if c, _ := get("/@me/lib/-/me-lib-0.0.1.tgz"); c != http.StatusNotFound {
		t.Fatalf("unknown tarball: %d", c)
	}
	ExtraScopes = nil
	if c, _ := get("/@me%2flib"); c != http.StatusFound {
		t.Fatalf("without ExtraScopes @me is public: %d", c)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// Audits never send names of registered folders, ssh-remote packages or
// the consona username upstream, even when the npmrc does not route them.
func TestAuditHidesLocalAndRemoteNames(t *testing.T) {
	routeScope(t, "o")
	ExtraScopes = func() map[string]bool { return map[string]bool{"me": true, "devbox": true} }
	defer func() { ExtraScopes = nil }()
	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
	}))
	defer up.Close()
	h := &handler{e: core.NewEngine(&liveSource{ver: "1.0.0"}, t.TempDir()), b: fakeBuilder{}, upstreamURL: up.URL, noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	defer srv.Close()
	post := func(path, body string) {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	post(bulkPath, `{"@me/lib":["1.0.0-local.1.haaaaaaaa"],"@devbox/tool":["3.0.0"],"@o/x":["1.0.0"],"lodash":["4.17.21"]}`)
	post(quickPath, `{"name":"@me/app","requires":{"@me/lib":"*","@devbox/tool":"*","lodash":"*"},"dependencies":{"@me/lib":{"version":"1.0.0"},"@devbox/tool":{"version":"3.0.0"},"lodash":{"version":"4.17.21"}}}`)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("upstream got %d requests", len(sent))
	}
	for _, b := range sent {
		for _, private := range []string{"@me/", "@devbox/", "@o/"} {
			if strings.Contains(b, private) {
				t.Fatalf("private name %s sent upstream: %s", private, b)
			}
		}
		if !strings.Contains(b, "lodash") {
			t.Fatalf("public name missing: %s", b)
		}
	}
}
