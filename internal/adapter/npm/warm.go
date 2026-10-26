package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// specName strips a version/range from an npm spec ("@o/n@^1" -> "@o/n").
func specName(spec string) string {
	if i := strings.LastIndex(spec, "@"); i > 0 {
		return spec[:i]
	}
	return spec
}

// privateRef returns the Ref for a routed scoped name.
func privateRef(name string, scopes map[string]bool) (core.Ref, bool) {
	ref, file, ok := parse("/" + name)
	if !ok || file != "" || !scopes[ref.Owner] {
		return core.Ref{}, false
	}
	return ref, true
}

// WarmNames lists routed private names in dir/package.json and in args.
func WarmNames(dir string, args []string, scopes map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if _, ok := privateRef(n, scopes); ok && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	var pj map[string]json.RawMessage
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(b, &pj) == nil {
		for _, k := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
			var deps map[string]string
			_ = json.Unmarshal(pj[k], &deps)
			for n := range deps {
				add(n)
			}
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "@") {
			add(specName(a))
		}
	}
	return out
}

// Warm requests the daemon to prefetch and prebuild private packages.
func Warm(baseURL string, names []string) {
	warm(baseURL, names)
}

func warm(baseURL string, names []string) {
	if len(names) == 0 {
		return
	}
	body, _ := json.Marshal(names)
	resp, err := http.Post(strings.TrimRight(baseURL, "/")+"/-/warm", "application/json", bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
	}
}

// defaultWarmTTL is how long a package counts as "warmed recently": a
// repeated packument or /-/warm request within this window does not walk
// it (or its dependencies) again. It matches the engine's version-list
// memo, so a second walk would only find memoised data anyway.
const defaultWarmTTL = 30 * time.Second

// warmWalk is one dependency walk. Its visited set is shared by every
// goroutine of the walk, so a dependency cycle (a -> b -> a) ends after
// each package has been visited once.
type warmWalk struct {
	mu      sync.Mutex
	visited map[string]bool
}

func newWarmWalk(seed ...string) *warmWalk {
	w := &warmWalk{visited: map[string]bool{}}
	for _, n := range seed {
		w.visited[n] = true
	}
	return w
}

// visit reports whether n is new to this walk (and marks it visited).
func (w *warmWalk) visit(n string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.visited[n] {
		return false
	}
	w.visited[n] = true
	return true
}

// claimWarm reports whether name should be warmed now: false when it was
// warmed within the handler's warm TTL (by this or any other walk).
func (h *handler) claimWarm(name string) bool {
	ttl := h.warmTTL
	if ttl == 0 {
		ttl = defaultWarmTTL
	}
	now := time.Now()
	h.warmMu.Lock()
	defer h.warmMu.Unlock()
	if h.warmed == nil {
		h.warmed = map[string]time.Time{}
	}
	if t, ok := h.warmed[name]; ok && now.Sub(t) < ttl {
		return false
	}
	if len(h.warmed) > 4096 { // bound the map: drop expired entries
		for k, t := range h.warmed {
			if now.Sub(t) >= ttl {
				delete(h.warmed, k)
			}
		}
	}
	h.warmed[name] = now
	return true
}

// warmRefs resolves packages in the background, then their routed private
// dependencies, so npm's later requests are served from memory. For
// packages with a build step it also starts building the latest version,
// the one npm most often picks. The whole walk shares one visited set,
// and packages warmed recently are skipped, so cycles terminate and
// repeated requests do not start new walks.
func (h *handler) warmRefs(names []string) {
	h.walk(names, privateScopes(), newWarmWalk())
}

func (h *handler) walk(names []string, scopes map[string]bool, w *warmWalk) {
	for _, n := range names {
		ref, ok := privateRef(n, scopes)
		if !ok || !w.visit(n) || !h.claimWarm(n) {
			continue
		}
		go func(n string, ref core.Ref) {
			if pkgs, err := h.e.Packages(context.Background(), h.b, ref, n); err == nil {
				h.prebuild(ref, n, pkgs)
				h.walk(depNames(pkgs, scopes), scopes, w)
			}
		}(n, ref)
	}
}

// prebuild builds the latest version in the background if it needs a build.
func (h *handler) prebuild(ref core.Ref, name string, pkgs []*core.Version) {
	if v := latest(pkgs); v != nil && v.Built == nil {
		go func() { _, _ = h.e.Build(context.Background(), h.b, ref, name, v) }()
	}
}

func latest(pkgs []*core.Version) *core.Version {
	var list []string
	for _, p := range pkgs {
		list = append(list, p.Version)
	}
	want := core.Latest(list)
	for _, p := range pkgs {
		if p.Version == want {
			return p
		}
	}
	return nil
}

// warmDeps prefetches routed private dependencies of name's versions (one
// walk; name itself counts as visited).
func (h *handler) warmDeps(name string, pkgs []*core.Version, scopes map[string]bool) {
	h.walk(depNames(pkgs, scopes), scopes, newWarmWalk(name))
}

// depNames lists the routed private dependencies of the given versions.
func depNames(pkgs []*core.Version, scopes map[string]bool) []string {
	seen := map[string]bool{}
	var names []string
	for _, p := range pkgs {
		var m struct {
			Deps map[string]string `json:"dependencies"`
			Opt  map[string]string `json:"optionalDependencies"`
			Peer map[string]string `json:"peerDependencies"`
		}
		_ = json.Unmarshal(p.Info.Manifest, &m)
		for _, d := range []map[string]string{m.Deps, m.Opt, m.Peer} {
			for n := range d {
				if _, ok := privateRef(n, scopes); ok && !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
	}
	return names
}
