package localcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindPnpmLockedVersion(t *testing.T) {
	content := `
lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      '@e2euser/lib-b':
        specifier: 1.0.0-local.1.habcdef12
        version: 1.0.0-local.1.habcdef12
      '@e2euser/lib-a':
        specifier: ^1.0.0
        version: 1.0.0-local.2.h34567890

packages:

  '@e2euser/lib-b@1.0.0-local.1.habcdef12':
    resolution: {integrity: sha512-xyz==}
`
	if got := findPnpmLockedVersion(content, "@e2euser/lib-b"); got != "1.0.0-local.1.habcdef12" {
		t.Errorf("got %q; want 1.0.0-local.1.habcdef12", got)
	}
	if got := findPnpmLockedVersion(content, "@e2euser/lib-a"); got != "1.0.0-local.2.h34567890" {
		t.Errorf("got %q; want 1.0.0-local.2.h34567890", got)
	}
	if got := findPnpmLockedVersion(content, "@e2euser/not-present"); got != "" {
		t.Errorf("got %q; want empty", got)
	}
}

func TestStaleLocalPnpm(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "cfg")
	home := filepath.Join(tmp, "home")
	t.Setenv("CONSONA_CONFIG_DIR", cfg)
	t.Setenv("CONSONA_HOME", home)

	// Initialize identity
	if code := cmdInit([]string{"--username", "testuser"}); code != 0 {
		t.Fatalf("init failed: %d", code)
	}

	// Create and register a local package
	pkgDir := filepath.Join(tmp, "my-lib")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@testuser/my-lib","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`module.exports = 1;`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRegister(t.Context(), []string{pkgDir}); code != 0 {
		t.Fatalf("register failed: %d", code)
	}

	appDir := filepath.Join(tmp, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Without lockfile
	if got := staleLocal(appDir); got != nil {
		t.Errorf("expected nil without lockfile; got %v", got)
	}

	// 2. With pnpm-lock.yaml having current version -> not stale
	lockPath := filepath.Join(appDir, "pnpm-lock.yaml")
	// Resolve current version
	stale := staleLocalPnpm(appDir)
	if len(stale) != 0 {
		t.Fatalf("expected not stale initially, got %v", stale)
	}

	// Write lockfile with older version
	lockContent := `
packages:
  '@testuser/my-lib@1.0.0-local.1.holdhash':
    resolution: {}
`
	if err := os.WriteFile(lockPath, []byte(lockContent), 0o600); err != nil {
		t.Fatal(err)
	}

	stale = staleLocal(appDir)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale package; got %v", stale)
	}
	if wantVer, ok := stale["@testuser/my-lib"]; !ok || !strings.HasPrefix(wantVer, "1.0.0-local.") {
		t.Errorf("got %v; expected @testuser/my-lib -> 1.0.0-local...", stale)
	}
}
