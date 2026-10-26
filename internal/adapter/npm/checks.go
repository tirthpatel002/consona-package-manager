package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// DefaultBuildWait bounds how long a packument request waits for the
// latest version's build (see awaitLatest). npm's own fetch timeout is
// 5 minutes; a typical prepare build takes seconds.
const DefaultBuildWait = 90 * time.Second

// BuildWait is how long a packument waits for the latest version's build:
// $CONSONA_BUILD_WAIT (a Go duration such as "30s"; "0" serves the
// packument at once, without that version's hashes), else
// DefaultBuildWait. It is read by the daemon.
func BuildWait() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CONSONA_BUILD_WAIT")); v != "" {
		if v == "0" {
			return -1
		}
		if d, err := time.ParseDuration(v); err == nil {
			if d <= 0 {
				return -1
			}
			return d
		}
	}
	return DefaultBuildWait
}

// awaitLatest makes sure the packument can carry dist.integrity and
// dist.shasum for the latest version (the one npm most often picks) when
// it has a build step and is not built yet: it waits for the build (an
// in-flight build is joined, not repeated) up to the build wait. If the
// wait runs out the build goes on in the background and the packument is
// served without that version's hashes, with a warning in the log; a
// failed build is reported when npm asks for the tarball. The returned
// slice is a copy: memoised versions are never modified.
func (h *handler) awaitLatest(ctx context.Context, ref core.Ref, name string, pkgs []*core.Version) []*core.Version {
	v := latest(pkgs)
	if v == nil || v.Built != nil || v.Info == nil || !v.Info.NeedsBuild {
		return pkgs
	}
	wait := h.buildWait
	if wait == 0 {
		wait = BuildWait()
	}
	if wait < 0 {
		return pkgs
	}
	type res struct {
		bl  *core.Built
		err error
	}
	ch := make(chan res, 1)
	go func() {
		bl, err := h.e.Build(context.WithoutCancel(ctx), h.b, ref, name, v)
		ch <- res{bl, err}
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil || r.bl == nil {
			return pkgs
		}
		out := make([]*core.Version, len(pkgs))
		for i, p := range pkgs {
			if p == v {
				c := *p
				c.Built = r.bl
				p = &c
			}
			out[i] = p
		}
		return out
	case <-t.C:
		logstore.Log(logstore.Entry{Event: logstore.EvInstallResolve, Level: "warn", Ecosystem: "npm", Package: name, Owner: ref.Owner,
			Repo: ref.Name, Version: v.Version, CommitSHA: v.Revision.Commit, Result: "build-wait-timeout",
			Message: fmt.Sprintf("%s@%s is still building after %s; its packument entry is served without dist.integrity (the lockfile gets it on a later install). Set CONSONA_BUILD_WAIT to wait longer", name, v.Version, wait)})
		return pkgs
	case <-ctx.Done():
		return pkgs
	}
}

// manifestName is the "name" in a version's own package.json.
func manifestName(v *core.Version) string {
	if v == nil || v.Info == nil {
		return ""
	}
	var m struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(v.Info.Manifest, &m)
	return m.Name
}

// checkName warns when the package.json at v names a different package
// than the one requested (consona serves it under the requested name,
// which is derived from the repo), and when v's git tag was moved after
// this daemon built it (lockfiles then fail with EINTEGRITY).
func (h *handler) checkName(ref core.Ref, name string, v *core.Version) {
	core.Touch(core.RedirectKey(ref.Owner, ref.Name)) // memoised versions: the source was not asked again
	if v == nil {
		return
	}
	if mn := manifestName(v); mn != "" && !strings.EqualFold(mn, name) {
		where := h.e.Source.Describe(ref)
		if i := strings.Index(where, " (also not in "); i > 0 { // chain: "github.com/o/r (also not in ...)"
			where = where[:i]
		}
		msg := fmt.Sprintf("%s@%s: package.json in %s says its name is %q; consona serves it as %s (the name comes from the repo). "+
			"Code that imports %q will not find it; rename the package or the repo so they match",
			name, v.Version, where, mn, name, mn)
		core.Warn("name|"+name+"|"+v.Revision.Commit, logstore.Entry{Event: logstore.EvSourceNameMismatch, Ecosystem: "npm", Package: name,
			Owner: ref.Owner, Repo: ref.Name, Version: v.Version, CommitSHA: v.Revision.Commit, Result: "name-mismatch", Message: msg,
			Fields: map[string]any{"requested": name, "manifest_name": mn}})
	}
	if old := h.e.MovedFrom(h.b, ref, v); len(old) > 0 {
		msg := fmt.Sprintf("%s@%s: tag %s now points at commit %.7s, but this version was built here before from commit %.7s; "+
			"a lockfile written then has the old integrity and npm will fail with EINTEGRITY. Run `npm install %s@%s` to update the lockfile, or move the tag back",
			name, v.Version, v.Revision.Tag, v.Revision.Commit, old[0], name, v.Version)
		core.Warn("moved|"+name+"|"+v.Version+"|"+v.Revision.Commit, logstore.Entry{Event: logstore.EvSourceTagMoved, Ecosystem: "npm", Package: name,
			Owner: ref.Owner, Repo: ref.Name, Version: v.Version, CommitSHA: v.Revision.Commit, Result: "tag-moved", Message: msg,
			Fields: map[string]any{"tag": v.Revision.Tag, "previous_commits": old}})
	}
}
