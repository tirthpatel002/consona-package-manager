package devsync

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTrackProject_DedupAndRecord(t *testing.T) {
	home := t.TempDir()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	pkg := "@test/pkg"
	if err := TrackProject(home, pkg, dir1); err != nil {
		t.Fatalf("TrackProject failed: %v", err)
	}

	projs, err := GetTrackedProjects(home, pkg)
	if err != nil {
		t.Fatalf("GetTrackedProjects failed: %v", err)
	}
	if len(projs) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projs))
	}
	if projs[0].Dir != dir1 {
		t.Fatalf("expected dir %s, got %s", dir1, projs[0].Dir)
	}
	firstSeen := projs[0].LastSeen

	// Sleep slightly to verify LastSeen update
	time.Sleep(10 * time.Millisecond)

	// Track dir2
	if err := TrackProject(home, pkg, dir2); err != nil {
		t.Fatalf("TrackProject failed: %v", err)
	}
	projs, _ = GetTrackedProjects(home, pkg)
	if len(projs) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projs))
	}

	// Track dir1 again: should dedup and update LastSeen
	time.Sleep(10 * time.Millisecond)
	if err := TrackProject(home, pkg, dir1); err != nil {
		t.Fatalf("TrackProject failed: %v", err)
	}
	projs, _ = GetTrackedProjects(home, pkg)
	if len(projs) != 2 {
		t.Fatalf("expected 2 projects after dedup, got %d", len(projs))
	}

	// dir1 should now be first (sorted newest first)
	if projs[0].Dir != dir1 {
		t.Fatalf("expected newest dir1 first, got %s", projs[0].Dir)
	}
	if !projs[0].LastSeen.After(firstSeen) {
		t.Fatalf("expected LastSeen to update, was %v, now %v", firstSeen, projs[0].LastSeen)
	}
}

func TestTrackProject_Cap(t *testing.T) {
	home := t.TempDir()
	baseDir := t.TempDir()
	pkg := "@test/cap-pkg"

	// Create 60 real directories
	for i := 0; i < 60; i++ {
		d := filepath.Join(baseDir, fmt.Sprintf("p%02d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := TrackProject(home, pkg, d); err != nil {
			t.Fatal(err)
		}
	}

	projs, err := GetTrackedProjects(home, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != MaxProjectsPerPackage {
		t.Fatalf("expected capped at %d, got %d", MaxProjectsPerPackage, len(projs))
	}
}

func TestTrackProject_NonExistentAndRelativeIgnored(t *testing.T) {
	home := t.TempDir()
	pkg := "@test/filter-pkg"

	// Relative path
	if err := TrackProject(home, pkg, "relative/path"); err != nil {
		t.Fatal(err)
	}
	// Non-existent path
	if err := TrackProject(home, pkg, filepath.Join(home, "non-existent-sub-dir")); err != nil {
		t.Fatal(err)
	}
	// Regular file instead of dir
	f := filepath.Join(home, "somefile.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := TrackProject(home, pkg, f); err != nil {
		t.Fatal(err)
	}

	projs, err := GetTrackedProjects(home, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 0 {
		t.Fatalf("expected 0 projects recorded, got %d", len(projs))
	}
}

func TestTrackProject_Concurrency(t *testing.T) {
	home := t.TempDir()
	baseDir := t.TempDir()
	pkg := "@test/concurrent"

	var dirs []string
	for i := 0; i < 10; i++ {
		d := filepath.Join(baseDir, fmt.Sprintf("c%d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			d := dirs[idx%len(dirs)]
			_ = TrackProject(home, pkg, d)
		}(i)
	}
	wg.Wait()

	projs, err := GetTrackedProjects(home, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != len(dirs) {
		t.Fatalf("expected %d deduped projects, got %d", len(dirs), len(projs))
	}
}
