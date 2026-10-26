package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

// warmRefs resolves packages in the background, then their routed private
// dependencies, so npm's later requests are served from memory. For
// packages with a build step it also starts building the latest version,
// the one npm most often picks.
func (h *handler) warmRefs(names []string) {
	scopes := RoutedScopes()
	for _, n := range names {
		ref, ok := privateRef(n, scopes)
		if !ok {
			continue
		}
		go func(n string, ref core.Ref) {
			if pkgs, err := h.e.Packages(context.Background(), h.b, ref, n); err == nil {
				h.prebuild(ref, n, pkgs)
				h.warmDeps(pkgs, scopes)
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

// warmDeps prefetches routed private dependencies of the given versions.
func (h *handler) warmDeps(pkgs []*core.Version, scopes map[string]bool) {
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
	if len(names) > 0 {
		h.warmRefs(names)
	}
}
