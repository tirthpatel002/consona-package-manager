// Package chain is the core.Source the daemon uses: it resolves a scoped
// name through every place a private package can live, in a fixed order,
// and remembers where each package came from.
//
// Order: 1. a folder registered on this device (local always wins),
// 2. a name in the folder map (a folder inside a GitHub repo, see
// internal/source/ghfolder), 3. explicitly added SSH remotes, 4. hosts
// auto-detected from ~/.ssh/config, 5. GitHub (@owner/repo). A name in a
// private scope that
// none of them has is a clear "not found"; it is never looked up on the
// public registry (unscoped and non-private names never reach consona's
// sources: the npm adapter redirects them to the public registry).
package chain

import (
	"context"
	"errors"
	"fmt"
	"github.com/tirthpatel002/consona-package-manager/internal/snapshot"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/source/ghfolder"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
	"github.com/tirthpatel002/consona-package-manager/internal/source/sshremote"
)

// Resolution records where a package version came from.
type Resolution struct {
	At      time.Time `json:"at"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Kind    string    `json:"kind"`   // local | ssh | github
	Where   string    `json:"where"`  // folder path, remote label, github.com/o/n
	Device  string    `json:"device"` // device that holds the source ("" for GitHub)
}

func (r Resolution) String() string {
	s := fmt.Sprintf("%s@%s <- %s %s", r.Name, r.Version, r.Kind, r.Where)
	if r.Device != "" {
		s += " [device " + r.Device + "]"
	}
	return s
}

// EventSink receives every resolution. It is the hook for a persistent
// log store (e.g. internal/logstore); the default only keeps the last
// ones in memory for `consona install` to print.
type EventSink interface{ Resolved(Resolution) }

// FailureSink is optionally implemented by an EventSink to also receive
// names no source could resolve, with what was tried.
type FailureSink interface {
	ResolveFailed(name string, tried []string, err error)
}

func (c *Chain) failed(name string, err error) error {
	if fs, ok := c.Sink.(FailureSink); ok && err != nil {
		fs.ResolveFailed(name, c.tried(), err)
	}
	return err
}

// tried lists the places a name was looked up, with per-remote state.
func (c *Chain) tried() []string {
	out := []string{"this device's registered folders: not registered"}
	if c.Folders != nil {
		out = append(out, "the folder map: not mapped")
	}
	if c.Remotes != nil {
		out = append(out, c.Remotes.Tried()...)
	}
	if c.GitHub == nil {
		out = append(out, "GitHub not available ("+errString(c.GitHubError)+")")
	}
	return out
}

// alsoTried appends the non-GitHub places to a GitHub error, keeping it
// wrapped (errors.Is/As still see core.ErrNotFound, *github.AccessError).
func (c *Chain) alsoTried(name string, err error) error {
	t := c.tried() // GitHub itself is described by err
	return fmt.Errorf("%s: %w\n  Also looked in: %s", name, err, strings.Join(t, "; "))
}

// Chain implements core.Source, core.Prebuilt and core.Live.
type Chain struct {
	Local       *local.Source
	Folders     *ghfolder.Source   // nil: no mapped GitHub folders
	Remotes     *sshremote.Manager // nil: no ssh remotes
	GitHub      core.Source        // nil: not logged in to GitHub
	ThisDevice  string             // short label of this device, for local resolutions
	Sink        EventSink          // optional
	GitHubError error              // why GitHub is nil, for messages

	mu     sync.Mutex
	recent []Resolution
}

func pkgName(ref core.Ref) string { return "@" + ref.Owner + "/" + ref.Name }

// Describe names where ref would be looked up.
func (c *Chain) Describe(ref core.Ref) string {
	if _, ok := c.Local.Lookup(ref); ok {
		return c.Local.Describe(ref)
	}
	places := c.Places(ref)
	if c.GitHub != nil { // "github.com/o/r (also not in this device's registered folders, ...)"
		return places[len(places)-1] + " (also not in " + strings.Join(places[:len(places)-1], ", ") + ")"
	}
	return strings.Join(places, ", ")
}

// Places lists where a name is looked up, in order.
func (c *Chain) Places(ref core.Ref) []string {
	parts := []string{"this device's registered folders"}
	if c.Folders != nil {
		parts = append(parts, "the folder map")
	}
	if c.Remotes != nil {
		if n := len(c.Remotes.Remotes()); n > 0 {
			parts = append(parts, fmt.Sprintf("%d ssh remote(s)", n))
		}
	}
	if c.GitHub != nil {
		parts = append(parts, c.GitHub.Describe(ref))
	} else {
		parts = append(parts, "GitHub (not available: "+errString(c.GitHubError)+")")
	}
	return parts
}

func errString(err error) string {
	if err == nil {
		return "no gh login"
	}
	return err.Error()
}

// Live: registered local folders are re-resolved on every request.
func (c *Chain) Live(ref core.Ref) bool { return c.Local.Live(ref) }

func (c *Chain) record(r Resolution) {
	r.At = time.Now()
	c.mu.Lock()
	c.recent = append(c.recent, r)
	if len(c.recent) > 500 {
		c.recent = c.recent[len(c.recent)-500:]
	}
	c.mu.Unlock()
	if c.Sink != nil {
		c.Sink.Resolved(r)
	}
}

// ResolutionsSince returns distinct resolutions (latest per name) after t.
func (c *Chain) ResolutionsSince(t time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := map[string]Resolution{}
	var order []string
	for _, r := range c.recent {
		if !r.At.After(t) {
			continue
		}
		if _, ok := last[r.Name]; !ok {
			order = append(order, r.Name)
		}
		last[r.Name] = r
	}
	out := make([]string, 0, len(order))
	for _, n := range order {
		out = append(out, last[n].String())
	}
	return out
}

func latestVersion(revs []core.Revision) string {
	var vs []string
	for _, r := range revs {
		vs = append(vs, r.Version)
	}
	return core.Latest(vs)
}

// Revisions resolves ref in order. GitHub is queried in parallel with the
// remote lookup so a GitHub package does not wait for ssh twice; a remote
// that has the name still wins.
func (c *Chain) Revisions(ctx context.Context, ref core.Ref) ([]core.Revision, error) {
	name := pkgName(ref)
	if snap, hash, ok := snapshot.FromContext(ctx); ok {
		return c.snapshotRevisions(ctx, ref, name, snap, hash)
	}
	if e, ok := c.Local.Lookup(ref); ok {
		revs, err := c.Local.Revisions(ctx, ref)
		if err != nil { // local always wins: do not fall through to other sources
			return nil, c.failed(name, fmt.Errorf("%w (local folders win: ssh remotes and GitHub were not tried)", err))
		}
		c.record(Resolution{Name: name, Version: latestVersion(revs), Kind: "local", Where: e.Path, Device: c.ThisDevice})
		return revs, nil
	}
	if c.Folders != nil {
		if _, ok := c.Folders.Lookup(ref); ok {
			revs, err := c.Folders.Revisions(ctx, ref)
			if err != nil { // the map is explicit: do not fall through to other sources
				return nil, c.failed(name, fmt.Errorf("%w (the folder map wins: ssh remotes and GitHub were not tried)", err))
			}
			c.record(Resolution{Name: name, Version: latestVersion(revs), Kind: "github-folder", Where: c.Folders.Describe(ref)})
			return revs, nil
		}
	}
	type ghResult struct {
		revs []core.Revision
		err  error
	}
	var ghc chan ghResult
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c.GitHub != nil {
		ghc = make(chan ghResult, 1)
		go func() {
			revs, err := c.GitHub.Revisions(gctx, ref)
			ghc <- ghResult{revs, err}
		}()
	}
	if c.Remotes != nil {
		if r, st := c.Remotes.Find(ctx, name); r != nil {
			cancel()
			revs, err := c.Remotes.Revisions(ctx, *r, ref)
			if err != nil {
				return nil, c.failed(name, fmt.Errorf("%s: listed on ssh remote %s, but resolving it there failed (GitHub not tried): %w", name, r.Label(), err))
			}
			{
				dev := ""
				if st.Device != nil {
					dev = st.Device.Short()
				}
				c.record(Resolution{Name: name, Version: latestVersion(revs), Kind: "ssh", Where: r.Label(), Device: dev})
			}
			return revs, err
		}
	}
	if ghc == nil {
		return nil, c.failed(name, fmt.Errorf("%s: %w; looked in: %s", name, core.ErrNotFound, strings.Join(c.tried(), "; ")))
	}
	g := <-ghc
	if g.err != nil {
		return nil, c.failed(name, c.alsoTried(name, g.err))
	}
	c.record(Resolution{Name: name, Version: latestVersion(g.revs), Kind: "github", Where: c.GitHub.Describe(ref)})
	return g.revs, nil
}

// snapshotRevisions resolves ref for a request under a project snapshot.
// Only the two sources a snapshot records are allowed: GitHub repos (their
// tags and head come from the snapshot, see github.Source.TagsAndHead) and
// folder-map entries that still match the mapping the snapshot recorded.
// Anything live (a registered local folder) or unrecorded (ssh remotes, a
// changed or new mapping) is a clear error, never a silent alternative.
func (c *Chain) snapshotRevisions(ctx context.Context, ref core.Ref, name string, snap *snapshot.Snapshot, hash string) ([]core.Revision, error) {
	id := hash
	if len(id) > 12 {
		id = id[:12]
	}
	if _, ok := c.Local.Lookup(ref); ok {
		return nil, c.failed(name, refuse("%s is a registered local folder, which is live and cannot be part of snapshot %s: run `consona unregister` for it, or install without the project's snapshot", name, id))
	}
	rec, recorded := snap.Folders[strings.ToLower(name)]
	if c.Folders != nil {
		if e, ok := c.Folders.Lookup(ref); ok {
			switch {
			case !recorded:
				return nil, c.failed(name, refuse("%s is mapped on this device to %s/%s but snapshot %s does not record that mapping: run `consona snapshot update`", name, e.Repo, e.Path, id))
			case !strings.EqualFold(e.Repo, rec.Repo) || e.Path != rec.Path:
				return nil, c.failed(name, refuse("%s is mapped to %s/%s but snapshot %s recorded %s/%s: the folder map changed; restore it or run `consona snapshot update`", name, e.Repo, e.Path, id, rec.Repo, rec.Path))
			}
			revs, err := c.Folders.Revisions(ctx, ref)
			if err != nil {
				return nil, c.failed(name, fmt.Errorf("%w (snapshot %s: no other source was tried)", err, id))
			}
			c.record(Resolution{Name: name, Version: latestVersion(revs), Kind: "github-folder", Where: c.Folders.Describe(ref) + " [snapshot " + id + "]"})
			return revs, nil
		}
	}
	if recorded {
		return nil, c.failed(name, refuse("snapshot %s recorded %s as folder %s of %s, but it is not mapped on this device: run `consona map add` or `consona map sync`", id, name, rec.Path, rec.Repo))
	}
	if c.GitHub == nil {
		return nil, c.failed(name, fmt.Errorf("%s: not logged in to GitHub, so the snapshot's repo cannot be read: %w", name, core.ErrAccess))
	}
	revs, err := c.GitHub.Revisions(ctx, ref)
	if err != nil {
		return nil, c.failed(name, fmt.Errorf("%w (snapshot %s: no other source was tried)", err, id))
	}
	c.record(Resolution{Name: name, Version: latestVersion(revs), Kind: "github", Where: c.GitHub.Describe(ref) + " [snapshot " + id + "]"})
	return revs, nil
}

// Fetch dispatches by revision kind.
func (c *Chain) Fetch(ctx context.Context, ref core.Ref, commit, dir string) error {
	if c.Folders != nil {
		if _, ok := c.Folders.Lookup(ref); ok {
			return c.Folders.Fetch(ctx, ref, commit, dir)
		}
	}
	switch {
	case strings.HasPrefix(commit, local.CommitPrefix):
		return c.Local.Fetch(ctx, ref, commit, dir)
	case strings.HasPrefix(commit, sshremote.CommitPrefix):
		return errors.New("remote packages are fetched prebuilt, not as source")
	case c.GitHub != nil:
		return c.GitHub.Fetch(ctx, ref, commit, dir)
	}
	return fmt.Errorf("%s: %w", pkgName(ref), core.ErrNotFound)
}

// PrebuiltInfo implements core.Prebuilt (remote revisions only).
func (c *Chain) PrebuiltInfo(ctx context.Context, ref core.Ref, rev core.Revision) (*core.Info, bool, error) {
	if c.Remotes == nil {
		return nil, false, nil
	}
	return c.Remotes.PrebuiltInfo(ctx, ref, rev)
}

// FetchPrebuilt implements core.Prebuilt (remote revisions only).
func (c *Chain) FetchPrebuilt(ctx context.Context, ref core.Ref, rev core.Revision, outDir string) (*core.Artifact, bool, error) {
	if c.Remotes == nil {
		return nil, false, nil
	}
	return c.Remotes.FetchPrebuilt(ctx, ref, rev, outDir)
}

// RemoteCache provides access to the shared build cache over SSH remotes.
func (c *Chain) RemoteCache() core.RemoteCache {
	if c.Remotes == nil {
		return nil
	}
	return c.Remotes
}

// PrivateScopes are scopes consona must treat as private besides those in
// the npmrc: registered folders' scopes and scopes seen on remotes.
func (c *Chain) PrivateScopes() map[string]bool {
	out := c.Local.Index.Scopes()
	if c.Folders != nil {
		for s := range c.Folders.Map.Scopes() {
			out[s] = true
		}
	}
	if c.Remotes != nil {
		for s := range c.Remotes.Scopes() {
			out[s] = true
		}
	}
	return out
}

// refuse builds an error for a request the snapshot cannot serve; the
// registry reports it as a conflict (not retryable), never as upstream failure.
func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{snapshot.ErrRefused}, a...)...)
}
