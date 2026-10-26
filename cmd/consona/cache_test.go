package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
)

func seedCacheEntry(t *testing.T, cacheDir, ecosystem, owner, repo, commit, version string, modTime time.Time, tarballSize int) (string, string) {
	t.Helper()
	entryDir := filepath.Join(cacheDir, ecosystem, owner, repo, commit, version)
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tarballName := fmt.Sprintf("%s-%s.tgz", repo, version)
	tarballPath := filepath.Join(entryDir, tarballName)
	tarballContent := make([]byte, tarballSize)
	for i := range tarballContent {
		tarballContent[i] = byte(i % 256)
	}
	if err := os.WriteFile(tarballPath, tarballContent, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(tarballPath, modTime, modTime)

	art := core.Artifact{
		Version:  version,
		File:     tarballName,
		Manifest: []byte(fmt.Sprintf(`{"name":"@%s/%s","version":"%s"}`, owner, repo, version)),
		SHA512:   "dGVzdA==",
	}
	artBytes, _ := json.Marshal(art)
	artPath := filepath.Join(entryDir, "artifact.json")
	if err := os.WriteFile(artPath, artBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(artPath, modTime, modTime)

	return entryDir, tarballPath
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	outChan := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		outChan <- buf.String()
	}()

	fn()
	w.Close()
	os.Stdout = oldStdout
	out := <-outChan
	r.Close()
	return out
}

func TestCacheList(t *testing.T) {
	home := t.TempDir()
	cfg := daemon.Config{Home: home}
	cacheDir := filepath.Join(home, "cache")

	now := time.Now()
	// Older entry: 2 hours ago
	seedCacheEntry(t, cacheDir, "npm", "acme", "ui", "11111111112222", "1.0.0", now.Add(-2*time.Hour), 1024)
	// Newer entry: 30 minutes ago
	seedCacheEntry(t, cacheDir, "npm", "acme", "core", "33333333334444", "2.5.0", now.Add(-30*time.Minute), 4096)

	out := captureStdout(t, func() {
		code := runCache(cfg, []string{"list"})
		if code != 0 {
			t.Fatalf("runCache list exited with code %d", code)
		}
	})

	if !strings.Contains(out, "PACKAGE") || !strings.Contains(out, "VERSION") || !strings.Contains(out, "COMMIT") {
		t.Fatalf("output missing table header:\n%s", out)
	}
	if !strings.Contains(out, "@acme/ui") || !strings.Contains(out, "@acme/core") {
		t.Fatalf("output missing packages:\n%s", out)
	}

	// Verify newest first: @acme/core should appear before @acme/ui
	coreIdx := strings.Index(out, "@acme/core")
	uiIdx := strings.Index(out, "@acme/ui")
	if coreIdx < 0 || uiIdx < 0 || coreIdx > uiIdx {
		t.Fatalf("expected @acme/core before @acme/ui in newest-first order:\n%s", out)
	}

	// Total size: 1024 + 4096 = 5120 B = 5.0 KB
	if !strings.Contains(out, "Total size:") || !strings.Contains(out, "2 entries") {
		t.Fatalf("output missing total size summary:\n%s", out)
	}
}

func TestCacheListJSON(t *testing.T) {
	home := t.TempDir()
	cfg := daemon.Config{Home: home}
	cacheDir := filepath.Join(home, "cache")

	now := time.Now()
	seedCacheEntry(t, cacheDir, "npm", "acme", "ui", "aaaaaaa1111111", "1.0.0", now.Add(-2*time.Hour), 1000)
	seedCacheEntry(t, cacheDir, "npm", "acme", "core", "bbbbbbb2222222", "2.0.0", now.Add(-10*time.Minute), 2000)

	out := captureStdout(t, func() {
		code := runCache(cfg, []string{"list", "--json"})
		if code != 0 {
			t.Fatalf("runCache list --json exited with code %d", code)
		}
	})

	var res cacheListOutput
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("failed unmarshaling json output: %v\noutput:\n%s", err, out)
	}

	if len(res.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(res.Entries))
	}
	if res.TotalSize != 3000 {
		t.Fatalf("total_size = %d, want 3000", res.TotalSize)
	}
	// Newest first
	if res.Entries[0].Package != "@acme/core" || res.Entries[1].Package != "@acme/ui" {
		t.Fatalf("expected @acme/core then @acme/ui, got: %+v", res.Entries)
	}
}

func TestCacheListEmpty(t *testing.T) {
	home := t.TempDir()
	cfg := daemon.Config{Home: home}

	// Plain list
	out := captureStdout(t, func() {
		code := runCache(cfg, []string{"list"})
		if code != 0 {
			t.Fatalf("code = %d", code)
		}
	})
	if !strings.Contains(out, "consona: cache is empty") {
		t.Fatalf("expected empty cache message, got %q", out)
	}

	// JSON list
	outJSON := captureStdout(t, func() {
		code := runCache(cfg, []string{"list", "--json"})
		if code != 0 {
			t.Fatalf("code = %d", code)
		}
	})
	var res cacheListOutput
	if err := json.Unmarshal([]byte(outJSON), &res); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if len(res.Entries) != 0 || res.TotalSize != 0 {
		t.Fatalf("unexpected json for empty cache: %+v", res)
	}
}

func TestCachePruneDryRun(t *testing.T) {
	home := t.TempDir()
	cfg := daemon.Config{Home: home}
	cacheDir := filepath.Join(home, "cache")

	now := time.Now()
	// Old: 40 days ago
	oldDir, oldTar := seedCacheEntry(t, cacheDir, "npm", "old", "pkg", "1111111111aaaa", "1.0.0", now.Add(-40*24*time.Hour), 5000)
	// New: 2 days ago
	newDir, newTar := seedCacheEntry(t, cacheDir, "npm", "new", "pkg", "2222222222bbbb", "2.0.0", now.Add(-2*24*time.Hour), 3000)

	out := captureStdout(t, func() {
		code := runCache(cfg, []string{"prune", "--older-than", "30d", "--dry-run"})
		if code != 0 {
			t.Fatalf("runCache prune --dry-run code = %d", code)
		}
	})

	if !strings.Contains(out, "would remove @old/pkg@1.0.0") {
		t.Fatalf("dry run missing would remove message:\n%s", out)
	}
	if strings.Contains(out, "@new/pkg") {
		t.Fatalf("dry run should not mention new package:\n%s", out)
	}
	if !strings.Contains(out, "would free") || !strings.Contains(out, "(dry run)") {
		t.Fatalf("dry run missing summary:\n%s", out)
	}

	// Verify NOTHING was deleted
	if _, err := os.Stat(oldTar); err != nil {
		t.Fatalf("old tarball was deleted during dry run: %v", err)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("old dir was deleted during dry run: %v", err)
	}
	if _, err := os.Stat(newTar); err != nil {
		t.Fatalf("new tarball was deleted during dry run: %v", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new dir was deleted during dry run: %v", err)
	}
}

func TestCachePrune(t *testing.T) {
	home := t.TempDir()
	cfg := daemon.Config{Home: home}
	cacheDir := filepath.Join(home, "cache")

	now := time.Now()
	// Old: 10 days ago (older than 7d)
	oldDir, _ := seedCacheEntry(t, cacheDir, "npm", "old", "pkg", "1111111111aaaa", "1.0.0", now.Add(-10*24*time.Hour), 5000)
	// New: 2 days ago (newer than 7d)
	newDir, newTar := seedCacheEntry(t, cacheDir, "npm", "new", "pkg", "2222222222bbbb", "2.0.0", now.Add(-2*24*time.Hour), 3000)

	out := captureStdout(t, func() {
		code := runCache(cfg, []string{"prune", "--older-than=7d"})
		if code != 0 {
			t.Fatalf("runCache prune code = %d", code)
		}
	})

	if !strings.Contains(out, "removed @old/pkg@1.0.0") {
		t.Fatalf("prune output missing removed entry:\n%s", out)
	}
	if !strings.Contains(out, "freed") {
		t.Fatalf("prune output missing freed summary:\n%s", out)
	}

	// Old entry must be deleted
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old directory should be deleted, err=%v", err)
	}
	// New entry must be intact
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new directory was deleted: %v", err)
	}
	if _, err := os.Stat(newTar); err != nil {
		t.Fatalf("new tarball was deleted: %v", err)
	}

	// Running prune again should report nothing to remove
	out2 := captureStdout(t, func() {
		code := runCache(cfg, []string{"prune", "--older-than", "7d"})
		if code != 0 {
			t.Fatalf("code = %d", code)
		}
	})
	if !strings.Contains(out2, "nothing to remove") {
		t.Fatalf("expected nothing to remove message, got: %s", out2)
	}
}
