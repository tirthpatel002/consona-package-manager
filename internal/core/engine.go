package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

// ErrBuild marks a failed package build; the message names the package,
// version and the log with the build output.
var ErrBuild = errors.New("build failed")

// BuildTailLines is how many trailing lines of a failed build's output
// are put into the error npm shows (the full output is in the log file).
var BuildTailLines = 20

// BuildTimeout bounds one package build (install + build scripts + pack).
var BuildTimeout = 10 * time.Minute

// Engine resolves a package through a Source and builds it with a Builder,
// caching artifacts on disk keyed by commit SHA (and published version).
type Engine struct {
	Source   Source
	CacheDir string
	TTL      time.Duration // how long a resolved version list is reused

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	memo  map[string]memoEntry
	errs  []reported
}

type memoEntry struct {
	at   time.Time
	pkgs []*Version
}

// Version is one published version of a package. Built is nil until the
// artifact exists (packages with a build step are built on first use).
type Version struct {
	Revision Revision
	Version  string
	Info     *Info
	Built    *Built
}

// Built is a built artifact on disk.
type Built struct {
	Artifact *Artifact
	Path     string // absolute path of the artifact file
}

// NewEngine returns an engine with a disk cache in cacheDir.
func NewEngine(src Source, cacheDir string) *Engine {
	return &Engine{Source: src, CacheDir: cacheDir, TTL: 30 * time.Second,
		locks: map[string]*sync.Mutex{}, memo: map[string]memoEntry{}}
}

func (e *Engine) lock(key string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		e.locks[key] = l
	}
	return l
}

func (e *Engine) base(b Builder, ref Ref) string {
	return filepath.Join(e.CacheDir, b.Ecosystem(), ref.Owner, ref.Name)
}

// Packages returns every published version of ref, ascending. Versions
// without a build step are packed now (cheap); others are built on demand
// by Build.
func (e *Engine) Packages(ctx context.Context, b Builder, ref Ref, name string) ([]*Version, error) {
	key := b.Ecosystem() + "|" + ref.String()
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	e.mu.Lock()
	m, ok := e.memo[key]
	e.mu.Unlock()
	if ok && time.Since(m.at) < e.TTL && !e.live(ref) {
		return m.pkgs, nil
	}
	revs, err := e.Source.Revisions(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, fmt.Errorf("%s: %w (no versions)", e.Source.Describe(ref), ErrNotFound)
	}
	base := e.base(b, ref)

	// Phase 1: manifests (cached per commit).
	infos := make([]*Info, len(revs))
	err = parallel(len(revs), func(i int) error {
		f := filepath.Join(base, revs[i].Commit, "info.json")
		var inf Info
		if bs, err := os.ReadFile(f); err == nil && json.Unmarshal(bs, &inf) == nil {
			infos[i] = &inf
			return nil
		}
		if pb, ok := e.Source.(Prebuilt); ok {
			got, ok, err := pb.PrebuiltInfo(ctx, ref, revs[i])
			if err != nil {
				return fmt.Errorf("%s@%s: %w", name, revs[i].Version, err)
			}
			if ok {
				infos[i] = got
				bs, _ := json.Marshal(got)
				return writeFileAtomic(f, bs)
			}
		}
		return e.withSource(ctx, ref, revs[i].Commit, func(dir string) error {
			got, err := b.Inspect(dir)
			if err != nil {
				return fmt.Errorf("%s@%s: %w", name, revs[i].Version, err)
			}
			infos[i] = got
			bs, _ := json.Marshal(got)
			return writeFileAtomic(f, bs)
		})
	})
	if err != nil {
		return nil, err
	}

	mv := make([]string, len(revs))
	for i, inf := range infos {
		mv[i] = inf.Version
	}
	vers := AssignVersions(revs, mv)
	out := make([]*Version, len(revs))
	for i := range revs {
		out[i] = &Version{Revision: revs[i], Version: vers[i], Info: infos[i], Built: e.cached(b, ref, revs[i].Commit, vers[i])}
	}
	// Phase 2: pack versions that need no build step (cheap, gives integrity).
	err = parallel(len(out), func(i int) error {
		if out[i].Built != nil || out[i].Info.NeedsBuild {
			return nil
		}
		bl, err := e.Build(ctx, b, ref, name, out[i])
		out[i].Built = bl
		return err
	})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.memo[key] = memoEntry{at: time.Now(), pkgs: out}
	e.mu.Unlock()
	return out, nil
}

func (e *Engine) cached(b Builder, ref Ref, commit, version string) *Built {
	dir := filepath.Join(e.base(b, ref), commit, version)
	var a Artifact
	if bs, err := os.ReadFile(filepath.Join(dir, "artifact.json")); err == nil && json.Unmarshal(bs, &a) == nil {
		return &Built{Artifact: &a, Path: filepath.Join(dir, a.File)}
	}
	return nil
}

// Build returns the artifact for v, building and caching it if needed.
// The returned Built is fresh; v itself is not modified.
func (e *Engine) Build(ctx context.Context, b Builder, ref Ref, name string, v *Version) (*Built, error) {
	dir := filepath.Join(e.base(b, ref), v.Revision.Commit, v.Version)
	l := e.lock("build|" + dir)
	l.Lock()
	defer l.Unlock()
	if bl := e.cached(b, ref, v.Revision.Commit, v.Version); bl != nil {
		return bl, nil
	}
	// A build must not die because one npm request was cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), BuildTimeout)
	defer cancel()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var art *Artifact
	started := time.Now()
	ev := logstore.Entry{Ecosystem: b.Ecosystem(), Package: name, Owner: ref.Owner, Repo: ref.Name, Version: v.Version, CommitSHA: v.Revision.Commit}
	prebuilt := false
	var err error
	if pb, ok := e.Source.(Prebuilt); ok {
		art, prebuilt, err = pb.FetchPrebuilt(ctx, ref, v.Revision, dir)
	}
	if !prebuilt && err == nil {
		if v.Info != nil && v.Info.NeedsBuild {
			st := ev
			st.Event, st.Level, st.Message = logstore.EvBuildStart, "info", "building with the package's own build scripts"
			logstore.Log(st)
		}
		err = e.withSource(ctx, ref, v.Revision.Commit, func(src string) error {
			var err error
			art, err = b.Build(ctx, src, BuildRequest{Name: name, Version: v.Version, Revision: v.Revision, OutDir: dir})
			return err
		})
	}
	ev.Event, ev.DurationMS, ev.Level, ev.Result = logstore.EvBuildFinish, time.Since(started).Milliseconds(), "info", "ok"
	if prebuilt {
		ev.Message = "prebuilt artifact fetched from source"
	}
	if err != nil {
		ev.Level, ev.Result, ev.Message = "error", "fail", err.Error()
	}
	if (v.Info != nil && v.Info.NeedsBuild && !prebuilt) || err != nil {
		logstore.Log(ev)
	} else {
		logstore.LogQuiet(ev)
	}
	if err != nil {
		what := fmt.Sprintf("for %s@%s (commit %.7s)", name, v.Version, v.Revision.Commit)
		var be *BuildError
		if errors.As(err, &be) {
			out := SanitizeOutput(be.Output)
			logf := filepath.Join(dir, "build-failed.log")
			_ = os.WriteFile(logf, out, 0o600)
			failed := be.Step + " failed"
			if be.Script != "" {
				failed = fmt.Sprintf("the package's %s failed (during %s)", be.Script, be.Step)
			}
			return nil, fmt.Errorf("%w %s: %s; last lines of the build output:\n%s\nfull output: %s",
				ErrBuild, what, failed, tailLines(out, BuildTailLines), logf)
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAccess) {
			return nil, err
		}
		return nil, fmt.Errorf("%w %s: %v", ErrBuild, what, err)
	}
	_ = os.Remove(filepath.Join(dir, "build-failed.log"))
	bs, _ := json.Marshal(art)
	if err := writeFileAtomic(filepath.Join(dir, "artifact.json"), bs); err != nil {
		return nil, err
	}
	return &Built{Artifact: art, Path: filepath.Join(dir, art.File)}, nil
}

// withSource fetches commit into a temp dir, runs f, and removes the dir.
func (e *Engine) withSource(ctx context.Context, ref Ref, commit string, f func(dir string) error) error {
	if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(e.CacheDir, "src-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "package")
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	if err := e.Source.Fetch(ctx, ref, commit, dir); err != nil {
		return err
	}
	return f(dir)
}

// parallel runs f(0..n-1) with bounded concurrency; returns the errors joined.
func parallel(n int, f func(int) error) error {
	sem := make(chan struct{}, 4)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = f(i)
		}(i)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), path)
}

type reported struct {
	at  time.Time
	msg string
}

// ReportError records a user-facing error so the CLI can summarise it.
func (e *Engine) ReportError(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = append(e.errs, reported{time.Now(), msg})
	if len(e.errs) > 100 {
		e.errs = e.errs[len(e.errs)-100:]
	}
}

// ErrorsSince returns distinct error messages reported after t.
func (e *Engine) ErrorsSince(t time.Time) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, r := range e.errs {
		if r.at.After(t) && !seen[r.msg] {
			seen[r.msg] = true
			out = append(out, r.msg)
		}
	}
	return out
}

// SanitizeOutput removes credentials from build output before it is shown
// or saved: token-shaped values, every credential consona has used
// (redact.AddSecret) and the values of the build environment's
// token/secret variables (a script may echo its environment).
func SanitizeOutput(b []byte) []byte {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		u := strings.ToUpper(k)
		for _, w := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "_AUTH", "APIKEY", "API_KEY", "CREDENTIAL"} {
			if strings.Contains(u, w) {
				redact.AddSecret(v)
				break
			}
		}
	}
	return []byte(redact.String(string(b)))
}

// tailLines returns the last n non-empty lines of out, each indented with
// "  | " and cut at 300 bytes.
func tailLines(out []byte, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		if len(l) > 300 {
			l = l[:300] + "…"
		}
		lines[i] = "  | " + l
	}
	if len(lines) == 0 {
		return "  | (no output)"
	}
	return strings.Join(lines, "\n")
}

// MovedFrom returns the commits other than v's for which this engine
// built v's version of ref before: a git tag that was moved (or deleted
// and re-created) after a lockfile recorded the old build's integrity.
// Only tagged versions of non-live sources are checked.
func (e *Engine) MovedFrom(b Builder, ref Ref, v *Version) []string {
	if v == nil || v.Revision.Tag == "" || v.Revision.Head || e.live(ref) {
		return nil
	}
	ents, err := os.ReadDir(e.base(b, ref))
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range ents {
		if !d.IsDir() || d.Name() == v.Revision.Commit {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.base(b, ref), d.Name(), v.Version, "artifact.json")); err == nil {
			out = append(out, d.Name())
		}
	}
	return out
}
