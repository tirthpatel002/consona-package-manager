package devsync

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatch_ChangeDetection(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "index.js")
	_ = os.WriteFile(f1, []byte("v1"), 0o644)

	var changeCount atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, WatchConfig{
			Dir:           dir,
			PollInterval:  20 * time.Millisecond,
			DebounceQuiet: 40 * time.Millisecond,
			OnChange: func() {
				changeCount.Add(1)
			},
		})
	}()

	time.Sleep(50 * time.Millisecond)

	// Modify file
	_ = os.WriteFile(f1, []byte("v2"), 0o644)

	// Wait for debounce and trigger
	time.Sleep(150 * time.Millisecond)
	if count := changeCount.Load(); count != 1 {
		t.Fatalf("expected 1 change trigger, got %d", count)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("unexpected watch err: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch did not exit promptly after context cancel")
	}
}

func TestWatch_Debounce(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "index.js")
	_ = os.WriteFile(f1, []byte("v1"), 0o644)

	var changeCount atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = Watch(ctx, WatchConfig{
			Dir:           dir,
			PollInterval:  15 * time.Millisecond,
			DebounceQuiet: 80 * time.Millisecond,
			OnChange: func() {
				changeCount.Add(1)
			},
		})
	}()

	time.Sleep(30 * time.Millisecond)

	// Make 3 rapid changes every 20ms (< 80ms debounce quiet period)
	for i := 0; i < 3; i++ {
		_ = os.WriteFile(f1, []byte{byte('a' + i)}, 0o644)
		time.Sleep(20 * time.Millisecond)
	}

	// Should not have fired yet
	if count := changeCount.Load(); count != 0 {
		t.Fatalf("expected 0 triggers during rapid changes, got %d", count)
	}

	// Wait for debounce period (80ms) + buffer
	time.Sleep(150 * time.Millisecond)
	if count := changeCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 debounced trigger, got %d", count)
	}
}

func TestWatch_SkipNodeModulesAndGit(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules", "pkg")
	git := filepath.Join(dir, ".git", "objects")
	_ = os.MkdirAll(nm, 0o755)
	_ = os.MkdirAll(git, 0o755)

	var changeCount atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = Watch(ctx, WatchConfig{
			Dir:           dir,
			PollInterval:  20 * time.Millisecond,
			DebounceQuiet: 40 * time.Millisecond,
			OnChange: func() {
				changeCount.Add(1)
			},
		})
	}()

	time.Sleep(50 * time.Millisecond)

	// Modify file in node_modules and .git
	_ = os.WriteFile(filepath.Join(nm, "mod.js"), []byte("junk"), 0o644)
	_ = os.WriteFile(filepath.Join(git, "obj"), []byte("junk"), 0o644)

	time.Sleep(150 * time.Millisecond)
	if count := changeCount.Load(); count != 0 {
		t.Fatalf("expected 0 triggers for node_modules/.git changes, got %d", count)
	}
}
