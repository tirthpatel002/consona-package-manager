package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/devsync"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outC <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-outC
}

func setupTestEnvironment(t *testing.T) (cfgDir, homeDir string) {
	t.Helper()
	cfgDir = t.TempDir()
	homeDir = t.TempDir()
	t.Setenv("CONSONA_CONFIG_DIR", cfgDir)
	t.Setenv("CONSONA_HOME", homeDir)
	return cfgDir, homeDir
}

func TestDevList_Empty(t *testing.T) {
	setupTestEnvironment(t)
	out := captureStdout(t, func() {
		code := run([]string{"dev", "--list"})
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "nothing registered on this device") {
		t.Fatalf("expected 'nothing registered on this device', got: %s", out)
	}
}

func TestDevList_WithRegisteredAndTracked(t *testing.T) {
	cfgDir, homeDir := setupTestEnvironment(t)

	// Register a package in localindex
	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@foo/bar","version":"1.0.0"}`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@foo/bar", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	// Track a project where node_modules exists, and one where it does not
	proj1 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj1, "node_modules", "@foo", "bar"), 0o755)
	if err := devsync.TrackProject(homeDir, "@foo/bar", proj1); err != nil {
		t.Fatal(err)
	}

	proj2 := t.TempDir()
	if err := devsync.TrackProject(homeDir, "@foo/bar", proj2); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		code := run([]string{"dev", "--list"})
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	})

	if !strings.Contains(out, "@foo/bar") {
		t.Fatalf("expected output to contain @foo/bar, got: %s", out)
	}
	if !strings.Contains(out, proj1+" (node_modules present)") {
		t.Fatalf("expected proj1 with node_modules present, got: %s", out)
	}
	if !strings.Contains(out, proj2+" (missing)") {
		t.Fatalf("expected proj2 with missing, got: %s", out)
	}
}

func TestDev_NotRegisteredError(t *testing.T) {
	setupTestEnvironment(t)
	sayBuf := captureSay(t, false)

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := run([]string{"dev", "@unknown/pkg"})
	_ = w.Close()
	os.Stderr = oldStderr

	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, r)
	errOut := errBuf.String()

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut, "@unknown/pkg is not registered on this device (register it with `consona register <folder>`)") {
		t.Fatalf("unexpected error message: %s (say: %s)", errOut, sayBuf.String())
	}
}

func TestDev_NoTargetProjectsFound(t *testing.T) {
	cfgDir, _ := setupTestEnvironment(t)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@foo/empty","version":"1.0.0"}`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@foo/empty", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := run([]string{"dev", "@foo/empty"})
	_ = w.Close()
	os.Stderr = oldStderr

	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, r)
	errOut := errBuf.String()

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut, "no target projects found for @foo/empty") {
		t.Fatalf("unexpected error message: %s", errOut)
	}
}

func TestDev_Once_SyncCorrectness(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	cfgDir, _ := setupTestEnvironment(t)
	sayBuf := captureSay(t, false)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@test/once-pkg","version":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`module.exports = "hello";`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@test/once-pkg", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	projDir := t.TempDir()
	code := run([]string{"dev", "@test/once-pkg", "--project", projDir, "--once"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (say: %s)", code, sayBuf.String())
	}

	// Verify file synced to projDir/node_modules/@test/once-pkg/index.js
	syncedFile := filepath.Join(projDir, "node_modules", "@test", "once-pkg", "index.js")
	content, err := os.ReadFile(syncedFile)
	if err != nil {
		t.Fatalf("synced file not found: %v", err)
	}
	if string(content) != `module.exports = "hello";` {
		t.Fatalf("unexpected content: %s", string(content))
	}

	// Verify say output format
	sayOutput := sayBuf.String()
	if !strings.Contains(sayOutput, "consona: dev: synced @test/once-pkg (2 files, 2 changed) ->") {
		t.Fatalf("unexpected say output: %s", sayOutput)
	}
}

func TestDev_Once_QuietMode(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	cfgDir, _ := setupTestEnvironment(t)
	sayBuf := captureSay(t, true)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@test/quiet","version":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`module.exports = 1;`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@test/quiet", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	projDir := t.TempDir()
	code := run([]string{"dev", "@test/quiet", "--project", projDir, "--once", "--quiet"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	// In quiet mode, sayBuf should have 0 bytes
	if sayBuf.Len() != 0 {
		t.Fatalf("expected empty say output in quiet mode, got: %s", sayBuf.String())
	}
}

func TestDev_Once_SymlinkRefused(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	cfgDir, _ := setupTestEnvironment(t)
	_ = captureSay(t, false)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@test/symrefuse","version":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`1`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@test/symrefuse", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	projDir := t.TempDir()
	targetDir := filepath.Join(projDir, "node_modules", "@test", "symrefuse")
	_ = os.MkdirAll(filepath.Dir(targetDir), 0o755)
	otherDir := t.TempDir()
	_ = os.Symlink(otherDir, targetDir)

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := run([]string{"dev", "@test/symrefuse", "--project", projDir, "--once"})
	_ = w.Close()
	os.Stderr = oldStderr

	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, r)
	errOut := errBuf.String()

	// Should fail/exit 1 because the only target project was refused
	if code != 1 {
		t.Fatalf("expected exit code 1 when all target projects are refused, got %d", code)
	}

	if !strings.Contains(errOut, "is a symlink pointing to") {
		t.Fatalf("expected symlink refusal message on stderr, got: %s", errOut)
	}
}

func TestDev_Once_QuietMode_ErrorsStillVisible(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	cfgDir, _ := setupTestEnvironment(t)
	sayBuf := captureSay(t, true)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@test/symrefuse-quiet","version":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`1`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@test/symrefuse-quiet", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	projDir := t.TempDir()
	targetDir := filepath.Join(projDir, "node_modules", "@test", "symrefuse-quiet")
	_ = os.MkdirAll(filepath.Dir(targetDir), 0o755)
	otherDir := t.TempDir()
	_ = os.Symlink(otherDir, targetDir)

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := run([]string{"dev", "@test/symrefuse-quiet", "--project", projDir, "--once", "--quiet"})
	_ = w.Close()
	os.Stderr = oldStderr

	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, r)
	errOut := errBuf.String()

	if code != 1 {
		t.Fatalf("expected exit code 1 when all target projects are refused, got %d", code)
	}

	// In quiet mode, sayBuf should have 0 bytes (no progress lines)
	if sayBuf.Len() != 0 {
		t.Fatalf("expected empty say output in quiet mode, got: %s", sayBuf.String())
	}

	// But stderr must still contain the error!
	if !strings.Contains(errOut, "is a symlink pointing to") {
		t.Fatalf("expected symlink refusal error on stderr even in quiet mode, got: %s", errOut)
	}
}

func TestDev_AutomaticTargetResolutionFromTracked(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	cfgDir, homeDir := setupTestEnvironment(t)
	sayBuf := captureSay(t, false)

	idx := &localindex.Store{Dir: cfgDir}
	pkgDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@auto/pkg","version":"1.0.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`module.exports = "auto";`), 0o644)
	if _, err := idx.Add(localindex.Entry{Name: "@auto/pkg", Path: pkgDir, DeviceID: "dev1"}, false); err != nil {
		t.Fatal(err)
	}

	// Project with existing node_modules/@auto/pkg
	projDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(projDir, "node_modules", "@auto", "pkg"), 0o755)
	if err := devsync.TrackProject(homeDir, "@auto/pkg", projDir); err != nil {
		t.Fatal(err)
	}

	// Running dev without --project should automatically pick up projDir
	code := run([]string{"dev", "@auto/pkg", "--once"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (say: %s)", code, sayBuf.String())
	}

	syncedFile := filepath.Join(projDir, "node_modules", "@auto", "pkg", "index.js")
	content, err := os.ReadFile(syncedFile)
	if err != nil {
		t.Fatalf("synced file not found: %v", err)
	}
	if string(content) != `module.exports = "auto";` {
		t.Fatalf("unexpected content: %s", string(content))
	}
}
