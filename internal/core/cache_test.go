package core

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCacheKey(t *testing.T) {
	valid := []struct {
		key                                     string
		ecosystem, owner, repo, commit, version string
		file                                    string
	}{
		{
			key:       "npm/acme/ui/abc123456789/1.0.0",
			ecosystem: "npm", owner: "acme", repo: "ui", commit: "abc123456789", version: "1.0.0",
		},
		{
			key:       "npm/my-scope/my-pkg/9876543210ab/2.1.0-beta.1",
			ecosystem: "npm", owner: "my-scope", repo: "my-pkg", commit: "9876543210ab", version: "2.1.0-beta.1",
		},
		{
			key:       "npm/owner/repo/local-0123456789abcdef/0.0.1",
			ecosystem: "npm", owner: "owner", repo: "repo", commit: "local-0123456789abcdef", version: "0.0.1",
		},
		{
			key:       "npm/acme/ui/abc123456789/1.0.0/ui-1.0.0.tgz",
			ecosystem: "npm", owner: "acme", repo: "ui", commit: "abc123456789", version: "1.0.0", file: "ui-1.0.0.tgz",
		},
		{
			key:       "npm/acme/ui/abc123456789/ui-1.0.0.tgz",
			ecosystem: "npm", owner: "acme", repo: "ui", commit: "abc123456789", version: "ui-1.0.0.tgz",
		},
	}

	for _, tc := range valid {
		eco, o, r, c, v, f, err := ValidateCacheKey(tc.key)
		if err != nil {
			t.Errorf("ValidateCacheKey(%q) unexpected error: %v", tc.key, err)
			continue
		}
		if eco != tc.ecosystem || o != tc.owner || r != tc.repo || c != tc.commit || v != tc.version || f != tc.file {
			t.Errorf("ValidateCacheKey(%q) = (%q, %q, %q, %q, %q, %q), want (%q, %q, %q, %q, %q, %q)",
				tc.key, eco, o, r, c, v, f, tc.ecosystem, tc.owner, tc.repo, tc.commit, tc.version, tc.file)
		}
	}

	invalid := []string{
		"",
		"../../../etc/passwd",
		"npm/../../etc/passwd",
		"npm/acme/ui/../abc1234567/1.0.0",
		"npm/acme/ui/abc1234567/..",
		"npm/acme/ui/abc1234567/1.0.0/..",
		"/npm/acme/ui/abc1234567/1.0.0",
		`\npm\acme\ui\abc1234567\1.0.0`,
		"npm/acme/ui/abc1234567/1.0.0/",
		"npm/acme/ui/abc1234567",           // 4 components
		"npm/acme/ui",                      // 3 components
		"npm/acme/ui/c/1/2/3",              // 7 components
		"npm//ui/abc1234567/1.0.0",         // empty owner
		"npm/acme//abc1234567/1.0.0",       // empty repo
		"npm/acme/ui//1.0.0",               // empty commit
		"npm/acme/ui/abc1234567/",          // empty version
		"npm/acme/ui/abc123/1.0.0",         // commit too short (<7 chars)
		"npm/acme/ui/abc1234567/1.0.0\x00", // null byte
		"npm/acme/ui/abc 1234567/1.0.0",    // space
		"npm/acme/ui/abc\t1234567/1.0.0",   // tab
		"npm/acme/ui/abc\n1234567/1.0.0",   // newline
		"npm/acme/ui/abc;rm/1.0.0",         // semicolon
	}

	for _, key := range invalid {
		_, _, _, _, _, _, err := ValidateCacheKey(key)
		if err == nil {
			t.Errorf("ValidateCacheKey(%q) expected error, got nil", key)
		} else if !errors.Is(err, ErrInvalidCacheKey) {
			t.Errorf("ValidateCacheKey(%q) expected ErrInvalidCacheKey, got %v", key, err)
		}
	}
}

func createTestTarball(t *testing.T, path string, pkgJSON string) {
	if t != nil {
		t.Helper()
	}
	f, err := os.Create(path)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	hdr := &tar.Header{
		Name: "package/package.json",
		Mode: 0o644,
		Size: int64(len(pkgJSON)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	if _, err := tw.Write([]byte(pkgJSON)); err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
}

func TestInspectTarball(t *testing.T) {
	dir := t.TempDir()

	// 1. Valid tarball matching name
	tarPath := filepath.Join(dir, "valid.tgz")
	createTestTarball(t, tarPath, `{"name":"@acme/ui","version":"1.0.0"}`)
	manifest, err := InspectTarball(tarPath, "@acme/ui")
	if err != nil {
		t.Fatalf("InspectTarball valid failed: %v", err)
	}
	if len(manifest) == 0 {
		t.Fatal("InspectTarball returned empty manifest")
	}

	// 2. Mismatched name
	_, err = InspectTarball(tarPath, "@acme/other")
	if err == nil {
		t.Fatal("InspectTarball expected error for mismatched package name, got nil")
	}

	// 3. Tarball missing package.json
	missingPath := filepath.Join(dir, "missing.tgz")
	mf, err := os.Create(missingPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(mf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "package/index.js", Size: 4})
	_, _ = tw.Write([]byte("test"))
	tw.Close()
	gw.Close()
	mf.Close()
	_, err = InspectTarball(missingPath, "@acme/ui")
	if err == nil {
		t.Fatal("InspectTarball expected error for missing package.json, got nil")
	}

	// 4. Corrupted file
	corruptPath := filepath.Join(dir, "corrupt.tgz")
	_ = os.WriteFile(corruptPath, []byte("not-gzip-data"), 0o644)
	_, err = InspectTarball(corruptPath, "@acme/ui")
	if err == nil {
		t.Fatal("InspectTarball expected error for corrupt gzip, got nil")
	}
}

func TestBuildCacheKey(t *testing.T) {
	key := BuildCacheKey("npm", "acme", "ui", "abc1234567", "1.0.0")
	want := "npm/acme/ui/abc1234567/1.0.0"
	if key != want {
		t.Fatalf("BuildCacheKey = %q, want %q", key, want)
	}
}
