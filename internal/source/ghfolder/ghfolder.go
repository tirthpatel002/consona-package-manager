// Package ghfolder is a core.Source for packages that live in a folder of
// a GitHub repo instead of a repo of their own. The device's folder map
// (internal/packmap, `consona map`) says which repo and folder a package
// name is served from, so one private repo (a monorepo) can serve many
// packages without being split and without publishing anything.
//
// Versions are the repo's version tags, shared by every folder in it,
// plus per-folder tags: a tag "<name>@v1.2.3" (the full mapped name)
// serves version 1.2.3 for that folder alone and wins over a repo-wide
// tag of the same version, so one repo's folders can release
// independently. An untagged repo serves its default-branch head like
// any other GitHub source. Only the mapped
// folder is extracted from the commit tarball, so the package builds
// exactly as if it were a repo of its own (its package.json is at the
// extracted root). The whole tarball is downloaded; files outside the
// folder never land on disk.
//
// A dependency on another mapped package name resolves through the daemon
// the same way, so packages in one repo can require each other by their
// normal names, with no per-consumer registry overrides.
package ghfolder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/packmap"
)

// GitHub is the repo-level API the source delegates to
// (*github.Source in production, a fake in tests).
type GitHub interface {
	Describe(ref core.Ref) string
	TagsAndHead(ctx context.Context, ref core.Ref) (tags []core.Tag, head string, err error)
	FetchPath(ctx context.Context, ref core.Ref, commit, prefix, dir string) error
}

// Source serves packages mapped to folders inside GitHub repos.
type Source struct {
	Map *packmap.Store
	Gh  GitHub
}

func name(ref core.Ref) string { return "@" + ref.Owner + "/" + ref.Name }

// Lookup returns the map entry for ref.
func (s *Source) Lookup(ref core.Ref) (packmap.Entry, bool) {
	e, ok, _ := s.Map.Lookup(name(ref))
	return e, ok
}

// Describe names the repo and folder ref is served from.
func (s *Source) Describe(ref core.Ref) string {
	if e, ok := s.Lookup(ref); ok {
		return "github.com/" + e.Repo + " (folder " + e.Path + ")"
	}
	return "GitHub folder map (" + name(ref) + " not mapped)"
}

func repoRef(e packmap.Entry) core.Ref {
	o, r := e.RepoRef()
	return core.Ref{Owner: o, Name: r}
}

// Revisions lists the mapped package's revisions: tags naming this
// package ("<name>@v1.2.3", the full mapped name) serve that folder
// alone, so one repo's folders can release independently; the repo's
// plain semver tags are shared by every folder, and with no plain tag
// the default-branch head is served as 0.0.0-<sha>, exactly like a
// repo-level GitHub source.
func (s *Source) Revisions(ctx context.Context, ref core.Ref) ([]core.Revision, error) {
	e, ok := s.Lookup(ref)
	if !ok {
		return nil, fmt.Errorf("%s: %w (not in the folder map)", name(ref), core.ErrNotFound)
	}
	tags, head, err := s.Gh.TagsAndHead(ctx, repoRef(e))
	if err != nil {
		return nil, err
	}
	revs := folderRevisions(tags, e.Name, head)
	// Cache identity: a name remapped to another folder of the same repo
	// at the same commit must not reuse the previous folder's cache.
	// Stamp a copy: the delegate may share its returned slice.
	out := make([]core.Revision, len(revs))
	for i, r := range revs {
		r.SourceID = "ghfolder/" + e.Repo + "/" + e.Path
		out[i] = r
	}
	return out, nil
}

// Fetch extracts only the mapped folder of the repo at commit into dir.
func (s *Source) Fetch(ctx context.Context, ref core.Ref, commit, dir string) error {
	e, ok := s.Lookup(ref)
	if !ok {
		return fmt.Errorf("%s: %w (not in the folder map)", name(ref), core.ErrNotFound)
	}
	if err := s.Gh.FetchPath(ctx, repoRef(e), commit, e.Path, dir); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return fmt.Errorf("%s: folder %s in github.com/%s has no package.json (commit %.7s)", name(ref), e.Path, e.Repo, commit)
	}
	return nil
}

// folderTagVersion parses a per-folder tag "<name>@v1.2.3": the package
// part must be the full mapped name (case-insensitive; mapped names are
// lowercase) and the version a full semver with an optional "v", the
// same rule as repo-wide tags.
func folderTagVersion(tag, name string) (string, bool) {
	i := strings.LastIndex(tag, "@")
	if i <= 0 || !strings.EqualFold(tag[:i], name) {
		return "", false
	}
	v := strings.TrimPrefix(tag[i+1:], "v")
	if !core.ValidVersion(v) {
		return "", false
	}
	return v, true
}

// folderRevisions composes the served revisions from the repo's tags:
// per-folder tags for name, then the repo-wide revisions (plain semver
// tags, or the default-branch head when there are none). A per-folder
// tag wins over a repo-wide tag of the same version; the result is
// sorted ascending like core.RevisionsFromTags.
func folderRevisions(tags []core.Tag, name, head string) []core.Revision {
	var rest []core.Tag
	seen := map[string]bool{}
	var out []core.Revision
	for _, t := range tags {
		v, ok := folderTagVersion(t.Name, name)
		if !ok {
			rest = append(rest, t)
			continue
		}
		if t.Commit == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, core.Revision{Version: v, Tag: t.Name, Commit: t.Commit})
	}
	for _, r := range core.RevisionsFromTags(rest, head) {
		if seen[r.Version] {
			continue
		}
		seen[r.Version] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return core.CompareVersions(out[i].Version, out[j].Version) < 0 })
	return out
}
