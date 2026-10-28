package chain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/packmap"
	"github.com/tirthpatel002/consona-package-manager/internal/snapshot"
	"github.com/tirthpatel002/consona-package-manager/internal/source/ghfolder"
)

type fakeFolderGH struct{ seen []string }

func (f *fakeFolderGH) TagsAndHead(ctx context.Context, r core.Ref) ([]core.Tag, string, error) {
	f.seen = append(f.seen, r.String())
	if snap, _, ok := snapshot.FromContext(ctx); ok {
		return snap.TagsAndHead(r)
	}
	return []core.Tag{{Name: "v9.9.9", Commit: strings.Repeat("9", 40)}}, "", nil
}
func (f *fakeFolderGH) Describe(r core.Ref) string                                        { return "github.com/" + r.String() }
func (f *fakeFolderGH) FetchPath(context.Context, core.Ref, string, string, string) error { return nil }

func snapCtx(t *testing.T, folders map[string]snapshot.Folder) (context.Context, *snapshot.Snapshot) {
	t.Helper()
	c := strings.Repeat("a", 40)
	s := &snapshot.Snapshot{Version: 1, Scopes: []string{"acme", "rem"},
		Repos:   map[string]snapshot.Repo{"acme/mono": {Head: c, Tags: map[string]string{"v1.0.0": c}}, "acme/gh-only": {Head: c, Tags: map[string]string{"v1.0.0": c}}},
		Folders: folders}
	data, err := snapshot.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	s, err = snapshot.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.With(context.Background(), snapshot.Hash(data), s), s
}

func TestSnapshotRejectsLiveLocalAndSkipsRemotes(t *testing.T) {
	c, gh, _, dir := newChain(t, "echo '{}'") // @acme/lib is a registered local folder
	ctx, _ := snapCtx(t, nil)
	_, err := c.Revisions(ctx, core.Ref{Owner: "acme", Name: "lib"})
	if err == nil || !strings.Contains(err.Error(), "registered local folder") || errors.Is(err, core.ErrNotFound) {
		t.Fatalf("live local folder must be refused clearly: %v", err)
	}
	if gh.calls.Load() != 0 {
		t.Fatal("github consulted after refusing the local folder")
	}
	// A GitHub package resolves from GitHub only; the ssh remote is never asked.
	revs, err := c.Revisions(ctx, core.Ref{Owner: "acme", Name: "gh-only"})
	if err != nil || len(revs) != 1 {
		t.Fatalf("gh-only: %v %v", revs, err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "calls")); serr == nil {
		t.Fatal("ssh remote was contacted during a snapshot request")
	}
}

func TestSnapshotBindsFolderMapping(t *testing.T) {
	c, _, _, dir := newChain(t, "")
	c.Local.Index.Remove("@acme/lib")
	mapDir := filepath.Join(dir, "mapcfg")
	m := &packmap.Store{Dir: mapDir}
	fg := &fakeFolderGH{}
	c.Folders = &ghfolder.Source{Map: m, Gh: fg}
	ref := core.Ref{Owner: "acme", Name: "ui"}
	rec := map[string]snapshot.Folder{"@acme/ui": {Repo: "acme/mono", Path: "packages/ui"}}
	ctx, _ := snapCtx(t, rec)

	// Mapped exactly as recorded: served from the frozen state, not the live v9.9.9.
	if err := m.Add(packmap.Entry{Name: "@acme/ui", Repo: "acme/mono", Path: "packages/ui"}); err != nil {
		t.Fatal(err)
	}
	revs, err := c.Revisions(ctx, ref)
	if err != nil || len(revs) != 1 || revs[0].Version != "1.0.0" {
		t.Fatalf("recorded mapping: %v %v", revs, err)
	}
	// Remapped to another folder of the same repo: refused (would mix folders under frozen commits).
	if err := m.Add(packmap.Entry{Name: "@acme/ui", Repo: "acme/mono", Path: "packages/other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revisions(ctx, ref); err == nil || !strings.Contains(err.Error(), "folder map changed") {
		t.Fatalf("remap must fail: %v", err)
	}
	// Unmapped now but recorded: refused, and not silently read as a repo-level package.
	if err := m.Remove("@acme/ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revisions(ctx, ref); err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("unmapped must fail: %v", err)
	}
	// Mapped now but not recorded: refused.
	ctx2, _ := snapCtx(t, nil)
	m.Add(packmap.Entry{Name: "@acme/ui", Repo: "acme/mono", Path: "packages/ui"})
	if _, err := c.Revisions(ctx2, ref); err == nil || !strings.Contains(err.Error(), "does not record") {
		t.Fatalf("unrecorded mapping must fail: %v", err)
	}
}
