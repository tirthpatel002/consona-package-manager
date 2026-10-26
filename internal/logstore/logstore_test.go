package logstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

const (
	fakePAT     = "github_pat_11ABCDEFG0hijklmnopqrstu_vwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopq9z9z"
	fakeClassic = "ghp_0123456789abcdefghijABCDEFGHIJ4321"
	fakeOpaque  = "opaque-secret-value-not-a-github-format"
)

type backend struct {
	name string
	open func(t *testing.T, dir string) Store
}

func backends() []backend {
	bs := []backend{{"jsonl", func(t *testing.T, dir string) Store {
		s, err := openJSONL(dir)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}}}
	if _, err := openSQLite(filepath.Join(os.TempDir(), "consona-probe-does-not-exist", "x.db")); err != ErrNoSQLite {
		bs = append(bs, backend{"sqlite", func(t *testing.T, dir string) Store {
			os.MkdirAll(dir, 0o700)
			s, err := openSQLite(filepath.Join(dir, "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}})
	}
	return bs
}

// The same contract for every backend.
func TestContract(t *testing.T) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			dir := t.TempDir()
			s := b.open(t, dir)
			defer s.Close()
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			var es []Entry
			for i := 0; i < 20; i++ {
				e := Entry{TS: base.Add(time.Duration(i) * time.Hour), Event: EvInstallResolve, Level: "info", Ecosystem: "npm",
					Package: "@acme/lib-" + fmt.Sprint(i%3), Owner: "acme", Repo: "lib", Version: "1.0.0", CommitSHA: "abc",
					CredentialSource: `named token "work"`, Result: "ok", DurationMS: int64(i), HTTPStatus: 200, Message: "m",
					Fields: map[string]any{"i": float64(i)}}
				if i%5 == 0 {
					e.Event, e.Level, e.Result, e.ErrorCode, e.HTTPStatus = EvCredentialFail, "warn", "fail", "unauthorized", 401
				}
				if i == 19 {
					e.Event, e.Level, e.Owner = EvBuildFinish, "error", "other"
				}
				es = append(es, e)
			}
			if err := s.Append(es[:10]...); err != nil {
				t.Fatal(err)
			}
			if err := s.Append(es[10:]...); err != nil {
				t.Fatal(err)
			}
			all, err := s.Query(Filter{})
			if err != nil || len(all) != 20 {
				t.Fatalf("%v %d", err, len(all))
			}
			for i := 1; i < len(all); i++ {
				if all[i].TS.Before(all[i-1].TS) || all[i].ID == all[i-1].ID {
					t.Fatal("not ascending / ids not unique")
				}
			}
			got := all[3]
			if got.TS.Location() != time.UTC || !got.TS.Equal(base.Add(3*time.Hour)) || got.Package != "@acme/lib-0" || got.CommitSHA != "abc" ||
				got.DurationMS != 3 || got.HTTPStatus != 200 || got.Fields["i"] != float64(3) || got.CredentialSource != `named token "work"` {
				t.Fatalf("round trip: %+v", got)
			}
			count := func(f Filter) int {
				r, err := s.Query(f)
				if err != nil {
					t.Fatal(err)
				}
				return len(r)
			}
			for name, c := range map[string]struct {
				f    Filter
				want int
			}{
				"package":   {Filter{Package: "@ACME/lib-1"}, 7},
				"owner @":   {Filter{Owner: "@acme"}, 19},
				"event pfx": {Filter{Event: "credential"}, 4},
				"event":     {Filter{Event: EvBuildFinish}, 1},
				"level":     {Filter{Level: "warn"}, 5},
				"errors":    {Filter{ErrorsOnly: true}, 5},
				"since":     {Filter{Since: base.Add(15 * time.Hour)}, 5},
				"until":     {Filter{Until: base.Add(4 * time.Hour)}, 5},
				"limit":     {Filter{Limit: 3}, 3},
			} {
				if n := count(c.f); n != c.want {
					t.Errorf("%s: %d, want %d", name, n, c.want)
				}
			}
			if l, _ := s.Query(Filter{Limit: 2}); l[1].TS != all[19].TS || l[0].TS != all[18].TS {
				t.Fatal("limit keeps the newest, ascending")
			}
			var buf bytes.Buffer
			if n, err := Export(s, &buf, "jsonl", Filter{}); err != nil || n != 20 || strings.Count(buf.String(), "\n") != 20 {
				t.Fatalf("export jsonl %d %v", n, err)
			}
			buf.Reset()
			var arr []Entry
			if _, err := Export(s, &buf, "json", Filter{Event: "build"}); err != nil || json.Unmarshal(buf.Bytes(), &arr) != nil || len(arr) != 1 {
				t.Fatalf("export json %v %s", err, buf.String())
			}
			in, err := s.Info()
			if err != nil || in.Backend != b.name || in.Count != 20 || in.Size == 0 || !in.Oldest.Equal(base) {
				t.Fatalf("info %+v %v", in, err)
			}
			// Retention by age.
			n, err := s.Prune(Retention{MaxAge: time.Since(base.Add(10*time.Hour - 30*time.Minute))})
			if err != nil || n != 10 || count(Filter{}) != 10 {
				t.Fatalf("prune age: %d %v %d", n, err, count(Filter{}))
			}
			if err := s.Clear(); err != nil || count(Filter{}) != 0 {
				t.Fatal("clear", err)
			}
			checkModes(t, dir)
		})
	}
}

func checkModes(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && p != dir && fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", p, fi.Mode().Perm())
		}
		if !fi.IsDir() && fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: %v", p, fi.Mode().Perm())
		}
		return nil
	})
}

// Token strings never reach either backend's files, whatever field they
// were put in.
func TestRedactionInBothBackends(t *testing.T) {
	redact.AddSecret(fakeOpaque)
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			dir := t.TempDir()
			s := b.open(t, dir)
			e := Entry{Event: EvAuthForward, Message: "Authorization: Bearer " + fakeClassic + " and " + fakePAT,
				CredentialSource: "env GH_TOKEN " + fakeClassic, ErrorCode: fakeOpaque, Package: fakePAT,
				Fields: map[string]any{"header": "token " + fakePAT, "nested": map[string]any{"v": fakeOpaque}, "npmrc": "//x/:_authToken=" + fakeOpaque}}
			if err := s.Append(normalize(e)); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Query(Filter{})
			j, _ := json.Marshal(got)
			s.Close()
			for _, secret := range []string{fakePAT, fakeClassic, fakeOpaque, fakeClassic[4:20], fakePAT[11:40]} {
				if strings.Contains(string(j), secret) {
					t.Fatalf("query returned a secret: %s", j)
				}
				filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
					if err == nil && !fi.IsDir() {
						data, _ := os.ReadFile(p)
						if bytes.Contains(data, []byte(secret)) {
							t.Errorf("%s contains a token", p)
						}
					}
					return nil
				})
			}
		})
	}
}

// Writers in several goroutines and several store instances (as the daemon
// and CLI processes are) do not lose or corrupt entries.
func TestConcurrentWriters(t *testing.T) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			dir := t.TempDir()
			stores := []Store{b.open(t, dir), b.open(t, dir), b.open(t, dir)}
			var wg sync.WaitGroup
			for w := 0; w < 6; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					s := stores[w%len(stores)]
					for i := 0; i < 50; i++ {
						if err := s.Append(normalize(Entry{Event: EvInstallFetch, Message: fmt.Sprintf("w%d-%d", w, i)})); err != nil {
							t.Error(err)
							return
						}
					}
				}(w)
			}
			wg.Wait()
			all, err := stores[0].Query(Filter{})
			if err != nil || len(all) != 300 {
				t.Fatalf("%v: %d entries", err, len(all))
			}
			seen := map[string]bool{}
			for _, e := range all {
				seen[e.Message] = true
			}
			if len(seen) != 300 {
				t.Fatalf("%d distinct", len(seen))
			}
			for _, s := range stores {
				s.Close()
			}
		})
	}
}

// Size retention keeps the store under the cap and drops the oldest.
func TestRetentionBySize(t *testing.T) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			s := b.open(t, t.TempDir())
			defer s.Close()
			big := strings.Repeat("x", 900)
			base := time.Now().Add(-time.Hour)
			for batch := 0; batch < 10; batch++ {
				var es []Entry
				for i := 0; i < 100; i++ {
					es = append(es, normalize(Entry{TS: base.Add(time.Duration(batch*100+i) * time.Millisecond), Event: EvInstallFetch, Message: big}))
				}
				if err := s.Append(es...); err != nil {
					t.Fatal(err)
				}
			}
			const max = 300 << 10
			if _, err := s.Prune(Retention{MaxAge: 24 * time.Hour, MaxSize: max}); err != nil {
				t.Fatal(err)
			}
			in, _ := s.Info()
			all, _ := s.Query(Filter{})
			if in.Size > max || len(all) == 0 || len(all) >= 1000 {
				t.Fatalf("size %d entries %d", in.Size, len(all))
			}
			if !all[len(all)-1].TS.Equal(base.Add(999 * time.Millisecond).UTC()) {
				t.Fatal("newest entry was dropped")
			}
		})
	}
}

func TestJSONLRotation(t *testing.T) {
	dir := t.TempDir()
	s, _ := openJSONL(dir)
	s.maxFile = 64 << 10
	big := strings.Repeat("y", 1000)
	for i := 0; i < 200; i++ {
		if err := s.Append(normalize(Entry{Event: EvInstallFetch, Message: big})); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.files()) < 2 {
		t.Fatalf("no rotation: %v", s.files())
	}
	if all, _ := s.Query(Filter{}); len(all) != 200 {
		t.Fatalf("%d", len(all))
	}
	checkModes(t, dir)
}

// Open picks SQLite, honours CONSONA_LOG_BACKEND and falls back to JSON
// lines when SQLite cannot be used (here: a corrupt database file).
func TestOpenFallback(t *testing.T) {
	if len(backends()) == 1 {
		t.Skip("built with nosqlite")
	}
	dir := t.TempDir()
	t.Setenv("CONSONA_LOG_BACKEND", "")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if in, _ := s.Info(); in.Backend != "sqlite" || in.Fallback != "" {
		t.Fatalf("%+v", in)
	}
	s.Close()

	t.Setenv("CONSONA_LOG_BACKEND", "jsonl")
	s, _ = Open(dir)
	if in, _ := s.Info(); in.Backend != "jsonl" {
		t.Fatalf("%+v", in)
	}
	s.Close()

	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "events.db"), bytes.Repeat([]byte("not a database "), 1000), 0o600)
	t.Setenv("CONSONA_LOG_BACKEND", "")
	s, err = Open(bad)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.Info()
	if in.Backend != "jsonl" || !strings.Contains(in.Fallback, "SQLite unavailable") {
		t.Fatalf("%+v", in)
	}
	if es, _ := s.Query(Filter{Event: EvLogstore}); len(es) != 1 || es[0].Level != "warn" {
		t.Fatalf("fallback not logged: %+v", es)
	}
	s.Close()

	t.Setenv("CONSONA_LOG_BACKEND", "sqlite")
	if _, err := Open(bad); err == nil {
		t.Fatal("forced sqlite on a corrupt db must fail")
	}
	t.Setenv("CONSONA_LOG_BACKEND", "mysql")
	if _, err := Open(dir); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestAsyncAndDefault(t *testing.T) {
	s, _ := openJSONL(t.TempDir())
	a := NewAsync(s)
	SetDefault(a, false)
	for i := 0; i < 1000; i++ {
		Log(Entry{Event: EvInstallFetch, Message: "hello " + fakeClassic})
	}
	a.Flush(context.Background())
	SetDefault(nil, false)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a.Append(Entry{}) // after Close: dropped, no panic
	all, _ := s.Query(Filter{})
	if len(all) != 1000 || strings.Contains(all[0].Message, fakeClassic) {
		t.Fatalf("%d %q", len(all), all[0].Message)
	}
	Log(Entry{Event: EvInstallFetch}) // no default store: no-op
}

func TestHumanLine(t *testing.T) {
	h := Human(Entry{Event: EvCredentialFail, Level: "warn", Owner: "acme", Repo: "lib", CredentialSource: `env GH_TOKEN`, HTTPStatus: 401, Message: "bad"})
	for _, want := range []string{"[credential.fail]", "WARN", "owner=acme", "status=401", "bad"} {
		if !strings.Contains(h, want) {
			t.Fatalf("%q missing %q", h, want)
		}
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 720 * time.Hour, "72h": 72 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseAge(in); err != nil || got != want {
			t.Fatalf("%s: %v %v", in, got, err)
		}
	}
	t.Setenv("CONSONA_LOG_MAX_AGE", "7d")
	t.Setenv("CONSONA_LOG_MAX_SIZE_MB", "5")
	if r := DefaultRetention(); r.MaxAge != 7*24*time.Hour || r.MaxSize != 5<<20 {
		t.Fatalf("%+v", r)
	}
}

// Performance: an install logs a few dozen events; appends through Async
// must not block the request path.
func BenchmarkAsyncLog(b *testing.B) {
	for _, be := range []string{"sqlite", "jsonl"} {
		b.Run(be, func(b *testing.B) {
			b.Setenv("CONSONA_LOG_BACKEND", be)
			s, err := Open(b.TempDir())
			if err != nil {
				b.Skip(err)
			}
			a := NewAsync(s)
			defer a.Close()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.Append(normalize(Entry{Event: EvInstallFetch, Package: "@a/b", Message: "x"}))
			}
			a.Flush(context.Background())
		})
	}
}

// BenchmarkQuery: `consona logs` against a store with 20k entries
// (newest 100 for one package, and errors only).
func BenchmarkQuery(b *testing.B) {
	for _, be := range []string{"sqlite", "jsonl"} {
		b.Run(be, func(b *testing.B) {
			b.Setenv("CONSONA_LOG_BACKEND", be)
			s, err := Open(b.TempDir())
			if err != nil {
				b.Skip(err)
			}
			defer s.Close()
			batch := make([]Entry, 0, 1000)
			for i := 0; i < 20000; i++ {
				e := Entry{Event: EvInstallFetch, Level: "info", Package: fmt.Sprintf("@a/p%d", i%50), Message: "fetched"}
				if i%97 == 0 {
					e.Level, e.Result = "error", "fail"
				}
				batch = append(batch, normalize(e))
				if len(batch) == cap(batch) {
					if err := s.Append(batch...); err != nil {
						b.Fatal(err)
					}
					batch = batch[:0]
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Query(Filter{Package: "@a/p7", Limit: 100}); err != nil {
					b.Fatal(err)
				}
				if _, err := s.Query(Filter{ErrorsOnly: true, Limit: 100}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
