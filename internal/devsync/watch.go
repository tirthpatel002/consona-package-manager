package devsync

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

// FileMeta holds size and modification time for change detection.
type FileMeta struct {
	Size  int64
	Mtime time.Time
}

// Snapshot is a map of relative file path to its metadata.
type Snapshot map[string]FileMeta

// TakeSnapshot walks dir, skipping node_modules and .git, and records (relpath, size, mtime).
func TakeSnapshot(dir string) (Snapshot, error) {
	snap := make(Snapshot)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		snap[rel] = FileMeta{
			Size:  info.Size(),
			Mtime: info.ModTime(),
		}
		return nil
	})
	return snap, err
}

// EqualSnapshots reports whether two snapshots have identical files, sizes and mtimes.
func EqualSnapshots(a, b Snapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va.Size != vb.Size || !va.Mtime.Equal(vb.Mtime) {
			return false
		}
	}
	return true
}

// WatchConfig configures the polling watcher.
type WatchConfig struct {
	Dir           string
	PollInterval  time.Duration
	DebounceQuiet time.Duration
	OnChange      func()
}

// DefaultPollInterval is 500ms.
const DefaultPollInterval = 500 * time.Millisecond

// DefaultDebounceQuiet is 300ms.
const DefaultDebounceQuiet = 300 * time.Millisecond

// Watch polls cfg.Dir for changes and fires cfg.OnChange after cfg.DebounceQuiet.
// Polling stops when ctx is cancelled.
func Watch(ctx context.Context, cfg WatchConfig) error {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.DebounceQuiet <= 0 {
		cfg.DebounceQuiet = DefaultDebounceQuiet
	}

	currentSnap, _ := TakeSnapshot(cfg.Dir)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	defer func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			newSnap, err := TakeSnapshot(cfg.Dir)
			if err != nil {
				continue
			}
			if !EqualSnapshots(currentSnap, newSnap) {
				currentSnap = newSnap
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.NewTimer(cfg.DebounceQuiet)
				debounceCh = debounceTimer.C
			}

		case <-debounceCh:
			debounceCh = nil
			if cfg.OnChange != nil {
				cfg.OnChange()
			}
			// Refresh snapshot after sync (build output might have been written)
			currentSnap, _ = TakeSnapshot(cfg.Dir)
		}
	}
}
