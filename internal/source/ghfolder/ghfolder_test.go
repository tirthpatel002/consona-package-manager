package ghfolder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/packmap"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
)

type fakeGh struct {
	tags     []core.Tag
	head     string
	tagsErr  error
	fetchErr error
	fetched  []string // prefixes fetched
	files    map[string]string
}

func (f *fakeGh) Describe(ref core.Ref) string { return "github.com/" + ref.String() }
func (f *fakeGh) TagsAndHead(_ context.Context, ref core.Ref) ([]core.Tag, string, error) {
	return f.tags, f.head, f.tagsErr
}
func (f *fakeGh) FetchPath(_ context.Context, ref core.Ref, commit, prefix, dir string) error {
	f.fetched = append(f.fetched, ref.String()+"@"+commit+":"+prefix)
	if f.fetchErr != nil {
		return f.fetchErr
	}
	for name, body := range f.files {
		dst := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func testSource(t *testing.T, gh GitHub, entries ...packmap.Entry) *Source {
	t.Helper()
	m := &packmap.Store{Dir: t.TempDir()}
	for _, e := range entries {
		if err := m.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	return &Source{Map: m, Gh: gh}
}

var widgetsRef = core.Ref{Owner: "acme", Name: "widgets-core"}

func TestRevisionsDelegatesToRepo(t *testing.T) {
	gh := &fakeGh{tags: []core.Tag{{Name: "v1.2.0", Commit: "abc123"}}}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	revs, err := s.Revisions(context.Background(), widgetsRef)
	if err != nil || len(revs) != 1 || revs[0].Version != "1.2.0" {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
	// The source identity rides along so a remap to another folder of the
	// same repo at the same commit never reuses this folder's cache.
	if revs[0].SourceID != "ghfolder/acme/widgets/packages/core" {
		t.Fatalf("SourceID: %q", revs[0].SourceID)
	}
	other := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/renamed"})
	revs2, err := other.Revisions(context.Background(), widgetsRef)
	if err != nil || revs2[0].SourceID == revs[0].SourceID {
		t.Fatalf("remap must change the source identity: %+v %v", revs2, err)
	}
	// The repo's identity, not the package's, is what GitHub sees.
	if d := s.Describe(widgetsRef); !strings.Contains(d, "acme/widgets") || !strings.Contains(d, "packages/core") {
		t.Fatalf("describe: %q", d)
	}
}

func TestRevisionsUnmapped(t *testing.T) {
	s := testSource(t, &fakeGh{})
	_, err := s.Revisions(context.Background(), widgetsRef)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, ok := s.Lookup(widgetsRef); ok {
		t.Fatal("unmapped ref must not be claimed")
	}
}

func TestFetchOnlyMappedFolder(t *testing.T) {
	gh := &fakeGh{files: map[string]string{
		"package.json": `{"name":"@acme/widgets-core","version":"1.0.0"}`,
		"index.js":     "module.exports = 1",
	}}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	dir := t.TempDir()
	if err := s.Fetch(context.Background(), widgetsRef, "abc123def", dir); err != nil {
		t.Fatal(err)
	}
	if gh.fetched[0] != "acme/widgets@abc123def:packages/core" {
		t.Fatalf("fetched: %v", gh.fetched)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatal("package.json must be at the extracted root")
	}
}

func TestFetchWithoutPackageJSON(t *testing.T) {
	gh := &fakeGh{files: map[string]string{"readme.md": "hi"}}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	err := s.Fetch(context.Background(), widgetsRef, "abc", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Fatalf("want missing package.json error, got %v", err)
	}
}

func TestFetchUnmapped(t *testing.T) {
	s := testSource(t, &fakeGh{})
	if err := s.Fetch(context.Background(), widgetsRef, "abc", t.TempDir()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// tarGz builds a GitHub-style tarball: one top-level dir, entries under it.
func tarGz(t *testing.T, top string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if strings.HasSuffix(name, "/") {
			if err := tw.WriteHeader(&tar.Header{Name: top + "/" + strings.TrimSuffix(name, "/"), Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractPath(t *testing.T) {
	for _, gh := range []struct {
		name  string
		files map[string]string
	}{
		{"nested", map[string]string{
			"packages/core/package.json":  `{}`,
			"packages/core/lib/index.js":  "x",
			"packages/other/package.json": `{"other":true}`,
			"root.txt":                    "root",
		}},
	} {
		t.Run(gh.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := github.ExtractPath(bytes.NewReader(tarGz(t, "acme-widgets-deadbeef", gh.files)), "packages/core", dir); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"package.json", "lib/index.js"} {
				if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
					t.Fatalf("missing %s: %v", want, err)
				}
			}
			for _, unwanted := range []string{"root.txt", "other", "packages"} {
				if _, err := os.Stat(filepath.Join(dir, unwanted)); !os.IsNotExist(err) {
					t.Fatalf("%s must not be extracted", unwanted)
				}
			}
		})
	}
}

func TestExtractPathNotFound(t *testing.T) {
	err := github.ExtractPath(bytes.NewReader(tarGz(t, "top", map[string]string{"a/b.txt": "x"})), "nope", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want folder-not-found error, got %v", err)
	}
}

func TestExtractPathRejectsUnsafe(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "x"
	if err := tw.WriteHeader(&tar.Header{Name: "top/pkgs/../../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	dir := t.TempDir()
	if err := github.ExtractPath(bytes.NewReader(buf.Bytes()), "pkgs", dir); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("want unsafe-path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("unsafe entry must not be written")
	}
}

func TestRevisionsPerFolderTags(t *testing.T) {
	gh := &fakeGh{tags: []core.Tag{
		{Name: "v1.0.0", Commit: "aaaa"},                       // repo-wide: shared by every folder
		{Name: "@acme/widgets-core@v1.1.0", Commit: "bbbb"},    // this folder, "v" prefix
		{Name: "@acme/widgets-core@1.2.0", Commit: "cccc"},     // this folder, bare
		{Name: "@acme/widgets-ui@v9.9.9", Commit: "dddd"},      // another package in the repo
		{Name: "widgets-core@1.3.0", Commit: "eeee"},           // bare folder name: not the full mapped name
		{Name: "@acme/widgets-core@notsemver", Commit: "ffff"}, // not a version
	}}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	revs, err := s.Revisions(context.Background(), widgetsRef)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range revs {
		got = append(got, r.Version+"="+r.Tag+"@"+r.Commit)
		if r.SourceID != "ghfolder/acme/widgets/packages/core" {
			t.Fatalf("SourceID: %q", r.SourceID)
		}
	}
	want := []string{
		"1.0.0=v1.0.0@aaaa",
		"1.1.0=@acme/widgets-core@v1.1.0@bbbb",
		"1.2.0=@acme/widgets-core@1.2.0@cccc",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRevisionsPerFolderTagWinsSameVersion(t *testing.T) {
	gh := &fakeGh{tags: []core.Tag{
		{Name: "v1.4.0", Commit: "shared"},
		{Name: "@acme/widgets-core@v1.4.0", Commit: "folder"},
	}}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	revs, err := s.Revisions(context.Background(), widgetsRef)
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
	if revs[0].Commit != "folder" || revs[0].Tag != "@acme/widgets-core@v1.4.0" {
		t.Fatalf("the per-folder tag must win its version: %+v", revs[0])
	}
}

func TestRevisionsPerFolderTagKeepsHeadFallback(t *testing.T) {
	// A repo with only per-folder tags still serves its default-branch
	// head, so untagged-style ranges (>=0.0.0-0) keep working.
	gh := &fakeGh{
		tags: []core.Tag{{Name: "@acme/widgets-core@v1.0.0", Commit: "bbbb"}},
		head: "deadbeefcafe",
	}
	s := testSource(t, gh, packmap.Entry{Name: "@acme/widgets-core", Repo: "acme/widgets", Path: "packages/core"})
	revs, err := s.Revisions(context.Background(), widgetsRef)
	if err != nil || len(revs) != 2 {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
	if revs[0].Version != "0.0.0-deadbee" || !revs[0].Head || revs[1].Version != "1.0.0" {
		t.Fatalf("revisions: %+v", revs)
	}
}

func TestFolderTagVersion(t *testing.T) {
	for _, c := range []struct {
		tag, name, want string
		ok              bool
	}{
		{"@acme/widgets-core@v1.2.3", "@acme/widgets-core", "1.2.3", true},
		{"@acme/widgets-core@1.2.3", "@acme/widgets-core", "1.2.3", true},
		{"@ACME/Widgets-Core@1.2.3", "@acme/widgets-core", "1.2.3", true}, // git tags are case-sensitive; matching is not
		{"@acme/widgets-core@1.2.3-rc.1", "@acme/widgets-core", "1.2.3-rc.1", true},
		{"widgets-core@1.2.3", "@acme/widgets-core", "", false},  // must be the full mapped name
		{"@acme/widgets-ui@1.2.3", "@acme/widgets-core", "", false},
		{"@acme/widgets-core@1.2", "@acme/widgets-core", "", false},
		{"v1.2.3", "@acme/widgets-core", "", false}, // repo-wide tag: not per-folder
	} {
		v, ok := folderTagVersion(c.tag, c.name)
		if v != c.want || ok != c.ok {
			t.Errorf("folderTagVersion(%q, %q) = %q, %v; want %q, %v", c.tag, c.name, v, ok, c.want, c.ok)
		}
	}
}
