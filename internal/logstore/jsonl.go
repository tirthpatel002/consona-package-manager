package logstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// jsonlStore keeps events in dir/events.jsonl, rotated to
// events-<unixnano>.jsonl at rotateSize. Writers (any process) hold an
// exclusive lock on dir/.lock; readers a shared one.
type jsonlStore struct {
	dir      string
	fallback string
	mu       sync.Mutex
	lastID   int64
	maxFile  int64 // rotation size (rotateSize; smaller in tests)
}

const rotateSize = 5 << 20

func openJSONL(dir string) (*jsonlStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	j := &jsonlStore{dir: dir, maxFile: rotateSize}
	// Verify we can lock and write.
	if err := j.withLock(true, func() error {
		f, err := os.OpenFile(j.current(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	}); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *jsonlStore) current() string { return filepath.Join(j.dir, "events.jsonl") }

func (j *jsonlStore) withLock(exclusive bool, f func() error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	lf, err := os.OpenFile(filepath.Join(j.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := lockFile(lf, exclusive); err != nil {
		return fmt.Errorf("locking %s: %w", j.dir, err)
	}
	defer unlockFile(lf)
	return f()
}

// files returns rotated files (oldest first) then the current file.
func (j *jsonlStore) files() []string {
	m, _ := filepath.Glob(filepath.Join(j.dir, "events-*.jsonl"))
	sort.Strings(m) // fixed-width unix nanos sort by time
	return append(m, j.current())
}

func (j *jsonlStore) Append(entries ...Entry) error {
	return j.withLock(true, func() error {
		f, err := os.OpenFile(j.current(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		enc := json.NewEncoder(w)
		for _, e := range entries {
			e = normalize(e)
			id := e.TS.UnixNano()
			if id <= j.lastID {
				id = j.lastID + 1
			}
			j.lastID = id
			e.ID = id
			if err := enc.Encode(e); err != nil {
				f.Close()
				return err
			}
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		st, _ := f.Stat()
		if err := f.Close(); err != nil {
			return err
		}
		if st != nil && st.Size() > j.maxFile {
			return os.Rename(j.current(), filepath.Join(j.dir, fmt.Sprintf("events-%020d.jsonl", time.Now().UnixNano())))
		}
		return nil
	})
}

func readJSONL(path string, f func(Entry)) error {
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			f(e)
		}
	}
	return sc.Err()
}

func (j *jsonlStore) Query(f Filter) ([]Entry, error) {
	var out []Entry
	err := j.withLock(false, func() error {
		for _, p := range j.files() {
			if err := readJSONL(p, func(e Entry) {
				if f.match(e) {
					out = append(out, e)
				}
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return limit(out, f.Limit), err
}

// Prune drops entries older than MaxAge (rewriting files), then whole
// oldest files until the total is under MaxSize.
func (j *jsonlStore) Prune(r Retention) (int64, error) {
	var removed int64
	err := j.withLock(true, func() error {
		cut := time.Now().Add(-r.MaxAge)
		for _, p := range j.files() {
			if r.MaxAge <= 0 {
				break
			}
			var keep []Entry
			n := 0
			if err := readJSONL(p, func(e Entry) {
				n++
				if !e.TS.Before(cut) {
					keep = append(keep, e)
				}
			}); err != nil {
				return err
			}
			if len(keep) == n {
				continue
			}
			removed += int64(n - len(keep))
			if len(keep) == 0 && p != j.current() {
				if err := os.Remove(p); err != nil {
					return err
				}
				continue
			}
			if err := rewrite(p, keep); err != nil {
				return err
			}
		}
		if r.MaxSize > 0 {
			files := j.files()
			var total int64
			sizes := map[string]int64{}
			for _, p := range files {
				if st, err := os.Stat(p); err == nil {
					sizes[p] = st.Size()
					total += st.Size()
				}
			}
			for _, p := range files {
				if total <= r.MaxSize {
					break
				}
				n := 0
				_ = readJSONL(p, func(Entry) { n++ })
				if p == j.current() {
					// Keep the newest entries of the current file that fit
					// in 3/4 of the cap (so pruning is not needed again at
					// the next append).
					var all []Entry
					_ = readJSONL(p, func(e Entry) { all = append(all, e) })
					budget := r.MaxSize*3/4 - (total - sizes[p])
					i := len(all)
					for i > 0 {
						b, _ := json.Marshal(all[i-1])
						if budget -= int64(len(b) + 1); budget < 0 {
							break
						}
						i--
					}
					keep := all[i:]
					removed += int64(len(all) - len(keep))
					if err := rewrite(p, keep); err != nil {
						return err
					}
					break
				}
				removed += int64(n)
				total -= sizes[p]
				if err := os.Remove(p); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return removed, err
}

func rewrite(path string, es []Entry) error {
	if len(es) == 0 && !strings.HasSuffix(path, "events.jsonl") {
		return os.Remove(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rewrite-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600)
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	for _, e := range es {
		if err := enc.Encode(e); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (j *jsonlStore) Clear() error {
	return j.withLock(true, func() error {
		for _, p := range j.files() {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		f, err := os.OpenFile(j.current(), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	})
}

func (j *jsonlStore) Info() (Info, error) {
	in := Info{Backend: "jsonl", Path: j.dir, Fallback: j.fallback}
	err := j.withLock(false, func() error {
		for _, p := range j.files() {
			if st, err := os.Stat(p); err == nil {
				in.Size += st.Size()
			}
			if err := readJSONL(p, func(e Entry) {
				in.Count++
				if in.Oldest.IsZero() || e.TS.Before(in.Oldest) {
					in.Oldest = e.TS
				}
				if e.TS.After(in.Newest) {
					in.Newest = e.TS
				}
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return in, err
}

func (j *jsonlStore) Close() error { return nil }
