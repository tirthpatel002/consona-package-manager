package devsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestSync_PreservesNestedNodeModules(t *testing.T) {
	proj := t.TempDir()
	pkg := "@my/lib"
	targetDir := filepath.Join(proj, "node_modules", "@my", "lib")

	// Set up existing package with nested node_modules (unhoisted dep)
	nestedDepDir := filepath.Join(targetDir, "node_modules", "unhoisted-dep")
	if err := os.MkdirAll(nestedDepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedDepFile := filepath.Join(nestedDepDir, "index.js")
	if err := os.WriteFile(nestedDepFile, []byte(`module.exports = "unhoisted";`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Existing package file
	if err := os.WriteFile(filepath.Join(targetDir, "index.js"), []byte(`module.exports = "v1";`), 0o644); err != nil {
		t.Fatal(err)
	}

	// New tarball (does not contain nested node_modules)
	tar1 := createTestTarball(t, []tarEntry{
		{name: "package/package.json", typeflag: tar.TypeReg, content: `{"name":"@my/lib","version":"1.1.0"}`},
		{name: "package/index.js", typeflag: tar.TypeReg, content: `module.exports = "v2";`},
	})

	res, err := SyncTarballToProject(tar1, pkg, proj)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Verify file counts do not count unhoisted node_modules
	if res.TotalFiles != 2 {
		t.Fatalf("expected 2 total files from tarball, got %d", res.TotalFiles)
	}
	// package.json was added (1), index.js changed (1)
	if res.ChangedFiles != 2 {
		t.Fatalf("expected 2 changed files, got %d", res.ChangedFiles)
	}

	// Verify package updated to v2
	content, err := os.ReadFile(filepath.Join(targetDir, "index.js"))
	if err != nil || string(content) != `module.exports = "v2";` {
		t.Fatalf("expected index.js v2, got %q (%v)", string(content), err)
	}

	// Verify nested node_modules survived intact!
	nestedContent, err := os.ReadFile(nestedDepFile)
	if err != nil {
		t.Fatalf("expected nested node_modules file to survive, but got error: %v", err)
	}
	if string(nestedContent) != `module.exports = "unhoisted";` {
		t.Fatalf("expected nested content unchanged, got %s", string(nestedContent))
	}
}

func TestSync_IgnoresTarballNodeModules(t *testing.T) {
	proj := t.TempDir()
	pkg := "@my/lib"

	// Tarball with content named node_modules
	tar1 := createTestTarball(t, []tarEntry{
		{name: "package/package.json", typeflag: tar.TypeReg, content: `{"name":"@my/lib","version":"1.0.0"}`},
		{name: "package/index.js", typeflag: tar.TypeReg, content: `module.exports = 1;`},
		{name: "package/node_modules/evil/index.js", typeflag: tar.TypeReg, content: `evil`},
	})

	res, err := SyncTarballToProject(tar1, pkg, proj)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if res.TotalFiles != 2 {
		t.Fatalf("expected 2 total files (ignoring node_modules in tarball), got %d", res.TotalFiles)
	}

	targetDir := filepath.Join(proj, "node_modules", "@my", "lib")
	// evil should NOT exist
	evilFile := filepath.Join(targetDir, "node_modules", "evil", "index.js")
	if _, err := os.Stat(evilFile); err == nil {
		t.Fatal("expected tarball node_modules entry to be ignored, but file exists")
	}
}

func TestSync_NestedNodeModulesSymlinkNotPreserved(t *testing.T) {
	proj := t.TempDir()
	pkg := "@my/lib"
	targetDir := filepath.Join(proj, "node_modules", "@my", "lib")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(targetDir, "index.js"), []byte("old"), 0o644)

	// Nested node_modules is a symlink pointing to otherDir
	otherDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(otherDir, "dep.js"), []byte("dep"), 0o644)
	if err := os.Symlink(otherDir, filepath.Join(targetDir, "node_modules")); err != nil {
		t.Fatal(err)
	}

	tar1 := createTestTarball(t, []tarEntry{
		{name: "package/index.js", typeflag: tar.TypeReg, content: "new"},
	})

	_, err := SyncTarballToProject(tar1, pkg, proj)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Target node_modules/ should NOT have the symlink preserved
	fi, err := os.Lstat(filepath.Join(targetDir, "node_modules"))
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Fatal("symlinked nested node_modules should not be preserved")
		}
	}
}

func TestBuildAndPack_NoNPMOnPATH(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()
	pkg := "@local/simple"

	_ = os.WriteFile(filepath.Join(srcDir, "package.json"), []byte(`{"name":"@local/simple","version":"1.2.3"}`), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "index.js"), []byte(`console.log("hello")`), 0o644)

	// Set PATH to empty temp dir so npm cannot be found
	t.Setenv("PATH", t.TempDir())

	_, err := BuildAndPack(context.Background(), srcDir, pkg, outDir)
	if err == nil {
		t.Fatal("expected error when npm is not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "npm is required for dev sync: npm not found on PATH") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestBuildAndPack_WithNPM(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}

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
}
