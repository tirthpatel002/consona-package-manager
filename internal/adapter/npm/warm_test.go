package npm

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// cycleSource serves @o/a -> @o/b -> @o/a (a dependency cycle) and counts
// how often each package's versions are listed.
type cycleSource struct {
	mu    sync.Mutex
	calls map[string]int
	name  map[string]string // repo -> name in its package.json (default: the requested one)
}

func (c *cycleSource) Describe(r core.Ref) string { return "cycle/" + r.String() }
func (c *cycleSource) Revisions(_ context.Context, r core.Ref) ([]core.Revision, error) {
	c.mu.Lock()
	c.calls[r.Name]++
	c.mu.Unlock()
	return core.RevisionsFromTags([]core.Tag{{Name: "v1.0.0", Commit: "c0ffee" + r.Name}}, ""), nil
}
func (c *cycleSource) Fetch(_ context.Context, r core.Ref, _, dir string) error {
	other := map[string]string{"a": "b", "b": "a"}[r.Name]
	name := "@o/" + r.Name
	if n := c.name[r.Name]; n != "" {
		name = n
	}
	pj := fmt.Sprintf(`{"name":%q,"version":"1.0.0","dependencies":{"@o/%s":"^1.0.0"}}`, name, other)
	return os.WriteFile(filepath.Join(dir, "package.json"), []byte(pj), 0o644)
}
func (c *cycleSource) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

// dirBuilder reads the manifest from the fetched source (no build step).
type dirBuilder struct{ fakeBuilder }

func (dirBuilder) Inspect(dir string) (*core.Info, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	return &core.Info{Version: "1.0.0", Manifest: b}, nil
}

// settle waits until n() stops changing for 300ms (or 5s pass).
func settle(n func() int) int {
	last, since := n(), time.Now()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v := n(); v != last {
			last, since = v, time.Now()
		} else if time.Since(since) > 300*time.Millisecond {
			break
		}
	}
	return last
}

// A dependency cycle (a -> b -> a) ends: the walk visits each package
// once, even with the engine's memo off (so every visit costs a source
// call, and the old recursive walk never stopped). Repeated warm
// requests within the warm TTL start no new walk.
func TestWarmCycleTerminates(t *testing.T) {
	routeScope(t, "o")
	src := &cycleSource{calls: map[string]int{}}
	e := core.NewEngine(src, t.TempDir())
	e.TTL = 0 // no memo: a looping walk would call the source forever
	h := &handler{e: e, b: dirBuilder{}, base: "http://127.0.0.1:1"}

	h.warmRefs([]string{"@o/a"})
	if n := settle(src.total); n != 2 {
		t.Fatalf("warm walk over a cycle listed versions %d times (calls %v), want 2 (a, b once each)", n, src.calls)
	}
	for i := 0; i < 5; i++ { // e.g. npm's packument requests, consona install's /-/warm
		h.warmRefs([]string{"@o/a", "@o/b"})
		h.warmDeps("@o/a", mustPackages(t, h, "a"), privateScopes())
	}
	if n := settle(src.total); n != 2+5 {
		t.Fatalf("repeated warms within the warm TTL: %d source calls (calls %v), want 7 (only the direct Packages calls)", n, src.calls)
	}

	// After the TTL a new walk runs, and still ends.
	h.warmTTL = time.Nanosecond
	before := src.total()
	h.warmRefs([]string{"@o/b"})
	if n := settle(src.total) - before; n != 2 {
		t.Fatalf("walk after the warm TTL: %d calls, want 2", n)
	}
}

func mustPackages(t *testing.T, h *handler, name string) []*core.Version {
	t.Helper()
	pkgs, err := h.e.Packages(context.Background(), h.b, core.Ref{Owner: "o", Name: name}, "@o/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// A package.json naming another package than the requested one is a
// warning: in the log store (normalized event), the daemon log, and the
// install summary (core.WarningsSince).
func TestNameMismatchWarning(t *testing.T) {
	logs := captureLogs(t)
	routeScope(t, "o")
	src := &cycleSource{calls: map[string]int{}, name: map[string]string{"a": "@o/something-else"}}
	e := core.NewEngine(src, t.TempDir())
	h := &handler{e: e, b: dirBuilder{}, noWarm: true}
	srv := newHandlerServer(t, h)
	start := time.Now()
	for i := 0; i < 3; i++ {
		getJSON(t, srv+"/@o%2fa")
	}
	getJSON(t, srv+"/@o%2fb")
	es := logs(logstore.EvSourceNameMismatch)
	if len(es) != 1 {
		t.Fatalf("want one name_mismatch event (logged once per package+commit), got %d", len(es))
	}
	if es[0].Level != "warn" || es[0].Package != "@o/a" || es[0].Fields["manifest_name"] != "@o/something-else" {
		t.Fatalf("bad event %+v", es[0])
	}
	w := core.WarningsSince(start)
	if len(w) != 1 || !strings.Contains(w[0], `"@o/something-else"`) || !strings.Contains(w[0], "serves it as @o/a") {
		t.Fatalf("install summary warnings %q", w)
	}
}

func newHandlerServer(t *testing.T, h *handler) string {
	srv := httptest.NewServer(h)
	h.base = srv.URL
	t.Cleanup(srv.Close)
	return srv.URL
}
