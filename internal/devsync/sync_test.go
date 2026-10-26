package devsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func createTestTarball(t *testing.T, entries []tarEntry) string {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "test.tgz")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o644,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
			Size:     int64(len(e.content)),
		}
		if e.typeflag == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return tarPath
}

type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	content  string
}

func TestPathEscape_Traversal(t *testing.T) {
	proj := t.TempDir()
	tarPath := createTestTarball(t, []tarEntry{
		{name: "package/../../evil.txt", typeflag: tar.TypeReg, content: "evil"},
	})

	_, err := SyncTarballToProject(tarPath, "@scope/pkg", proj)
	if err == nil {
		t.Fatal("expected error on path traversal tar entry, got nil")
	}
}

func TestPathEscape_Absolute(t *testing.T) {
	proj := t.TempDir()
	tarPath := createTestTarball(t, []tarEntry{
		{name: "/etc/passwd", typeflag: tar.TypeReg, content: "root"},
	})

	_, err := SyncTarballToProject(tarPath, "@scope/pkg", proj)
	if err == nil {
		t.Fatal("expected error on absolute path tar entry, got nil")
	}
}

func TestPathEscape_EscapingSymlink(t *testing.T) {
	proj := t.TempDir()
	tarPath := createTestTarball(t, []tarEntry{
		{name: "package/sym", typeflag: tar.TypeSymlink, linkname: "../../etc/shadow"},
	})

	_, err := SyncTarballToProject(tarPath, "@scope/pkg", proj)
	if err == nil {
		t.Fatal("expected error on escaping symlink tar entry, got nil")
	}
}

func TestPathEscape_AbsoluteSymlink(t *testing.T) {
	proj := t.TempDir()
	tarPath := createTestTarball(t, []tarEntry{
		{name: "package/sym", typeflag: tar.TypeSymlink, linkname: "/etc/hosts"},
	})

	_, err := SyncTarballToProject(tarPath, "@scope/pkg", proj)
	if err == nil {
		t.Fatal("expected error on absolute symlink tar entry, got nil")
	}
}

func TestSync_SymlinkedNodeModulesRefused(t *testing.T) {
	proj := t.TempDir()
	otherDir := t.TempDir()
	targetDir := filepath.Join(proj, "node_modules", "@scope", "pkg")
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherDir, targetDir); err != nil {
		t.Fatal(err)
	}

	tarPath := createTestTarball(t, []tarEntry{
		{name: "package/index.js", typeflag: tar.TypeReg, content: "console.log('hi')"},
	})

	_, err := SyncTarballToProject(tarPath, "@scope/pkg", proj)
	if err == nil {
		t.Fatal("expected error when node_modules/<pkg> is a symlink, got nil")
	}
	if !errors.Is(err, ErrSymlinkRefused) {
		t.Fatalf("expected ErrSymlinkRefused, got %v", err)
	}
}

func TestSync_AtomicSwapAndFileCounts(t *testing.T) {
	proj := t.TempDir()
	pkg := "@my/lib"

	// 1. Initial sync
	tar1 := createTestTarball(t, []tarEntry{
		{name: "package/package.json", typeflag: tar.TypeReg, content: `{"name":"@my/lib","version":"1.0.0"}`},
		{name: "package/index.js", typeflag: tar.TypeReg, content: `module.exports = 1;`},
		{name: "package/util.js", typeflag: tar.TypeReg, content: `module.exports = 'util';`},
	})

	res1, err := SyncTarballToProject(tar1, pkg, proj)
	if err != nil {
		t.Fatalf("sync 1 failed: %v", err)
	}
	if res1.TotalFiles != 3 {
		t.Fatalf("expected 3 total files, got %d", res1.TotalFiles)
	}
	if res1.ChangedFiles != 3 {
		t.Fatalf("expected 3 changed files on initial sync, got %d", res1.ChangedFiles)
	}

	// Verify file content in target dir
	targetDir := filepath.Join(proj, "node_modules", "@my", "lib")
	content, err := os.ReadFile(filepath.Join(targetDir, "index.js"))
	if err != nil || string(content) != `module.exports = 1;` {
		t.Fatalf("unexpected index.js content: %s (%v)", string(content), err)
	}

	// 2. Second sync: modify 1 file (index.js), delete 1 file (util.js), add 1 file (extra.js)
	// total = 3, changed = 3 (1 mod + 1 del + 1 add)
	tar2 := createTestTarball(t, []tarEntry{
		{name: "package/package.json", typeflag: tar.TypeReg, content: `{"name":"@my/lib","version":"1.0.0"}`},
		{name: "package/index.js", typeflag: tar.TypeReg, content: `module.exports = 2;`},
		{name: "package/extra.js", typeflag: tar.TypeReg, content: `module.exports = 'extra';`},
	})

	res2, err := SyncTarballToProject(tar2, pkg, proj)
	if err != nil {
		t.Fatalf("sync 2 failed: %v", err)
	}
	if res2.TotalFiles != 3 {
		t.Fatalf("expected 3 total files, got %d", res2.TotalFiles)
	}
	if res2.ChangedFiles != 3 {
		t.Fatalf("expected 3 changed files (1 mod + 1 add + 1 del), got %d", res2.ChangedFiles)
	}

	// Verify index.js updated
	content, _ = os.ReadFile(filepath.Join(targetDir, "index.js"))
	if string(content) != `module.exports = 2;` {
		t.Fatalf("expected index.js updated, got %s", string(content))
	}
	// Verify util.js was removed
	if _, err := os.Stat(filepath.Join(targetDir, "util.js")); err == nil {
		t.Fatal("expected util.js to be removed after sync 2")
	}
	// Verify extra.js exists
	if _, err := os.Stat(filepath.Join(targetDir, "extra.js")); err != nil {
		t.Fatal("expected extra.js to exist after sync 2")
	}
}

func TestBuildAndPack_PureGoFallback(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	pkg := "@local/simple"

	_ = os.WriteFile(filepath.Join(srcDir, "package.json"), []byte(`{"name":"@local/simple","version":"1.2.3"}`), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "index.js"), []byte(`console.log("hello")`), 0o644)

	tarPath, err := BuildAndPack(context.Background(), srcDir, pkg, outDir)
	if err != nil {
		t.Fatalf("BuildAndPack failed: %v", err)
	}
	if !filepath.IsAbs(tarPath) {
		t.Fatalf("expected absolute tarPath, got %s", tarPath)
	}
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatalf("tarball file does not exist: %v", err)
	}

	// Sync it into a project
	proj := t.TempDir()
	res, err := SyncTarballToProject(tarPath, pkg, proj)
	if err != nil {
		t.Fatalf("SyncTarballToProject failed: %v", err)
	}
	if res.TotalFiles != 2 {
		t.Fatalf("expected 2 files, got %d", res.TotalFiles)
	}
}
