// Package logstore is consona's structured event log: one normalized
// schema, two backends (SQLite first, rotated JSON-lines files as the
// fallback), retention by age and size, and redaction of every value
// before it is stored. It is separate from the token index and never
// holds a credential value.
package logstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

// Event names.
const (
	EvInstallResolve    = "install.resolve"
	EvInstallFetch      = "install.fetch"
	EvBuildStart        = "build.start"
	EvBuildFinish       = "build.finish"
	EvDaemonStart       = "daemon.start"
	EvDaemonStop        = "daemon.stop"
	EvRegistryRedirect  = "registry.redirect"
	EvAuditFilter       = "audit.filter"
	EvAuthForward       = "auth.forward"
	EvCredentialResolve = "credential.resolve"
	EvCredentialFail    = "credential.fail"
	EvTokenAdd          = "token.add"
	EvTokenRemove       = "token.remove"
	EvTokenTest         = "token.test"
	EvSetup             = "setup"
	EvLogstore          = "logstore"
	EvError             = "error"
)

// Events is the enumeration of valid event names.
var Events = []string{EvInstallResolve, EvInstallFetch, EvBuildStart, EvBuildFinish, EvDaemonStart, EvDaemonStop,
	EvRegistryRedirect, EvAuditFilter, EvAuthForward, EvCredentialResolve, EvCredentialFail,
	EvTokenAdd, EvTokenRemove, EvTokenTest, EvSetup, EvLogstore, EvError}

// Entry is one event. Both backends store exactly these fields.
type Entry struct {
	ID               int64          `json:"id"`
	TS               time.Time      `json:"ts"` // UTC, RFC 3339 with nanoseconds
	Level            string         `json:"level"`
	Event            string         `json:"event"`
	Ecosystem        string         `json:"ecosystem,omitempty"`
	Package          string         `json:"package,omitempty"`
	Owner            string         `json:"owner,omitempty"`
	Repo             string         `json:"repo,omitempty"`
	Version          string         `json:"version,omitempty"`
	CommitSHA        string         `json:"commit_sha,omitempty"`
	CredentialSource string         `json:"credential_source,omitempty"` // name/kind only
	Result           string         `json:"result,omitempty"`
	DurationMS       int64          `json:"duration_ms,omitempty"`
	HTTPStatus       int            `json:"http_status,omitempty"`
	ErrorCode        string         `json:"error_code,omitempty"`
	Message          string         `json:"message,omitempty"`
	Fields           map[string]any `json:"fields,omitempty"`
}

// Filter selects entries. Zero values match everything.
type Filter struct {
	Package, Owner string
	Event          string // exact name, or a prefix ending without '.' ("build" matches build.*)
	Level          string // minimum level: debug < info < warn < error
	Since, Until   time.Time
	ErrorsOnly     bool // level error or result fail
	Limit          int  // newest N (0 = all)
}

// Retention bounds the store.
type Retention struct {
	MaxAge  time.Duration // default 30 days
	MaxSize int64         // bytes, default 50 MB
}

// Info describes a store.
type Info struct {
	Backend  string // sqlite | jsonl
	Path     string
	Count    int64
	Size     int64
	Fallback string // why SQLite was not used ("" if it is)
	Oldest   time.Time
	Newest   time.Time
}

// Store is a backend.
type Store interface {
	Append(entries ...Entry) error
	Query(f Filter) ([]Entry, error)
	Prune(r Retention) (removed int64, err error)
	Clear() error
	Info() (Info, error)
	Close() error
}

// Export writes the entries matching f as JSON lines or a JSON array.
func Export(s Store, w io.Writer, format string, f Filter) (int, error) {
	es, err := s.Query(f)
	if err != nil {
		return 0, err
	}
	switch format {
	case "", "jsonl":
		enc := json.NewEncoder(w)
		for _, e := range es {
			if err := enc.Encode(e); err != nil {
				return 0, err
			}
		}
	case "json":
		if es == nil {
			es = []Entry{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(es); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unknown export format %q (jsonl or json)", format)
	}
	return len(es), nil
}

var levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// normalize fills defaults and redacts every string.
func normalize(e Entry) Entry {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	e.TS = e.TS.UTC()
	if _, ok := levels[e.Level]; !ok {
		e.Level = "info"
	}
	if e.Event == "" {
		e.Event = EvError
	}
	for _, p := range []*string{&e.Event, &e.Ecosystem, &e.Package, &e.Owner, &e.Repo, &e.Version, &e.CommitSHA,
		&e.CredentialSource, &e.Result, &e.ErrorCode, &e.Message} {
		*p = redact.String(*p)
	}
	if e.Fields != nil {
		b, err := json.Marshal(e.Fields)
		if err == nil {
			var m map[string]any
			if json.Unmarshal([]byte(redact.String(string(b))), &m) == nil {
				e.Fields = m
			} else {
				e.Fields = map[string]any{"redacted": true}
			}
		}
	}
	return e
}

func (f Filter) match(e Entry) bool {
	if f.Package != "" && !strings.EqualFold(e.Package, f.Package) {
		return false
	}
	if f.Owner != "" && !strings.EqualFold(strings.TrimLeft(e.Owner, "@"), strings.TrimLeft(f.Owner, "@")) {
		return false
	}
	if f.Event != "" && e.Event != f.Event && !strings.HasPrefix(e.Event, strings.TrimSuffix(f.Event, ".")+".") {
		return false
	}
	if f.Level != "" && levels[e.Level] < levels[f.Level] {
		return false
	}
	if !f.Since.IsZero() && e.TS.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.TS.After(f.Until) {
		return false
	}
	if f.ErrorsOnly && e.Level != "error" && e.Result != "fail" {
		return false
	}
	return true
}

// limit keeps the newest n of entries sorted ascending.
func limit(es []Entry, n int) []Entry {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].TS.Equal(es[j].TS) {
			return es[i].ID < es[j].ID
		}
		return es[i].TS.Before(es[j].TS)
	})
	if n > 0 && len(es) > n {
		es = es[len(es)-n:]
	}
	return es
}

// DefaultRetention reads CONSONA_LOG_MAX_AGE (e.g. 30d, 72h) and
// CONSONA_LOG_MAX_SIZE_MB; defaults 30 days and 50 MB.
func DefaultRetention() Retention {
	r := Retention{MaxAge: 30 * 24 * time.Hour, MaxSize: 50 << 20}
	if v := os.Getenv("CONSONA_LOG_MAX_AGE"); v != "" {
		if d, err := ParseAge(v); err == nil && d > 0 {
			r.MaxAge = d
		}
	}
	if v, err := strconv.ParseInt(os.Getenv("CONSONA_LOG_MAX_SIZE_MB"), 10, 64); err == nil && v > 0 {
		r.MaxSize = v << 20
	}
	return r
}

// ParseAge accepts Go durations plus a "d" (days) suffix.
func ParseAge(v string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(v)
}

// Dir is the log store directory inside consona's state dir.
func Dir(home string) string { return filepath.Join(home, "logs") }

// ErrNoSQLite is returned by openSQLite in nosqlite builds.
var ErrNoSQLite = errors.New("built without SQLite (nosqlite build tag)")

// Open opens the store in dir: SQLite (logs/events.db) unless
// CONSONA_LOG_BACKEND=jsonl, falling back to JSON-lines files
// (logs/jsonl/) if SQLite cannot be opened, created or migrated, is
// locked or corrupt, or the binary has no SQLite. The reason is kept in
// Info().Fallback and logged once.
func Open(dir string) (Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	backend := strings.ToLower(os.Getenv("CONSONA_LOG_BACKEND"))
	var why string
	switch backend {
	case "jsonl":
		why = "CONSONA_LOG_BACKEND=jsonl"
	case "", "sqlite":
		s, err := openSQLite(filepath.Join(dir, "events.db"))
		if err == nil {
			return s, nil
		}
		if backend == "sqlite" {
			return nil, fmt.Errorf("log store: SQLite requested (CONSONA_LOG_BACKEND=sqlite) but unavailable: %v", err)
		}
		why = "SQLite unavailable: " + redact.String(err.Error())
	default:
		return nil, fmt.Errorf("CONSONA_LOG_BACKEND=%q: use sqlite or jsonl", backend)
	}
	j, err := openJSONL(filepath.Join(dir, "jsonl"))
	if err != nil {
		return nil, err
	}
	j.fallback = why
	if backend != "jsonl" {
		warnOnce.Do(func() { log.Printf("consona: log store falling back to JSON lines (%s)", why) })
		_ = j.Append(Entry{Event: EvLogstore, Level: "warn", Result: "fallback", Message: "using JSON-lines log store: " + why})
	}
	return j, nil
}

var warnOnce sync.Once

// ---- process-wide default store ----

var (
	defMu   sync.RWMutex
	def     *Async
	echo    bool
	echoLog = log.Printf
)

// SetDefault makes s the store Log writes to (nil disables logging).
// With echoHuman, each event is also printed through the standard logger
// (the daemon's human-readable log) in the same redacted form.
func SetDefault(s *Async, echoHuman bool) {
	defMu.Lock()
	def, echo = s, echoHuman
	defMu.Unlock()
}

// Log records an event in the default store (no-op if none), and echoes
// it to the human-readable log if enabled.
func Log(e Entry) { logEntry(e, true) }

// LogQuiet records an event without echoing it (for per-request events
// the daemon log already shows as request lines).
func LogQuiet(e Entry) { logEntry(e, false) }

func logEntry(e Entry, echoIt bool) {
	e = normalize(e)
	defMu.RLock()
	s, ec := def, echo && echoIt
	defMu.RUnlock()
	if ec {
		echoLog("%s", Human(e))
	}
	if s != nil {
		s.Append(e)
	}
}

// Human is the one-line human form of an entry (used for the daemon log
// and `consona logs`).
func Human(e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", e.Event, strings.ToUpper(e.Level))
	for _, kv := range [][2]string{{"pkg", e.Package}, {"owner", e.Owner}, {"repo", e.Repo}, {"version", e.Version},
		{"commit", short(e.CommitSHA)}, {"cred", e.CredentialSource}, {"result", e.Result}, {"code", e.ErrorCode}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, " %s=%s", kv[0], quoteIf(kv[1]))
		}
	}
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, " status=%d", e.HTTPStatus)
	}
	if e.DurationMS != 0 {
		fmt.Fprintf(&b, " ms=%d", e.DurationMS)
	}
	if e.Message != "" {
		b.WriteString(" " + strings.ReplaceAll(e.Message, "\n", "\n    "))
	}
	return b.String()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func quoteIf(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return strconv.Quote(s)
	}
	return s
}

// Async batches appends off the caller's path: a buffered queue flushed
// by one goroutine (in one transaction for SQLite) every 100ms or 256
// entries. Close flushes.
type Async struct {
	S       Store
	ch      chan Entry
	done    chan struct{}
	flushc  chan chan struct{}
	dropped atomic.Int64
	mu      sync.RWMutex
	closed  bool
}

// NewAsync wraps s.
func NewAsync(s Store) *Async {
	a := &Async{S: s, ch: make(chan Entry, 8192), done: make(chan struct{}), flushc: make(chan chan struct{})}
	go a.loop()
	return a
}

// Append queues e (dropped, and counted, if the queue is full).
func (a *Async) Append(e Entry) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return
	}
	select {
	case a.ch <- e:
	default:
		a.dropped.Add(1)
	}
}

// Dropped is how many entries were dropped because the queue was full.
func (a *Async) Dropped() int64 { return a.dropped.Load() }

func (a *Async) loop() {
	defer close(a.done)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	var buf []Entry
	flush := func() {
		if len(buf) > 0 {
			if err := a.S.Append(buf...); err != nil {
				log.Printf("consona: log store write failed: %v", err)
			}
			buf = buf[:0]
		}
	}
	for {
		select {
		case e, ok := <-a.ch:
			if !ok {
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= 256 {
				flush()
			}
		case r := <-a.flushc:
			for drained := false; !drained; {
				select {
				case e := <-a.ch:
					buf = append(buf, e)
				default:
					drained = true
				}
			}
			flush()
			close(r)
		case <-t.C:
			flush()
		}
	}
}

// Flush writes everything queued so far.
func (a *Async) Flush(ctx context.Context) {
	r := make(chan struct{})
	select {
	case a.flushc <- r:
		select {
		case <-r:
		case <-ctx.Done():
		}
	case <-a.done:
	case <-ctx.Done():
	}
}

// Close flushes and closes the store.
func (a *Async) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	close(a.ch)
	a.mu.Unlock()
	<-a.done
	return a.S.Close()
}
