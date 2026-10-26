// Package local is a core.Source for package folders registered on this
// device (see localindex). Installs always get the folder's live state:
//
//   - The folder's content hash (every file except node_modules/, .git/ and
//     OS junk) is its revision ("local-<hash>"), so builds are cached by
//     content and redone only when something changed.
//   - The published version is "<package.json version>-local.<n>.h<hash8>",
//     where n goes up by one each time the content changes. A new content
//     is therefore always a new, higher version with a new tarball and a new
//     lockfile integrity; an unchanged folder keeps its version, so npm's
//     cache and lockfiles stay stable. (Build metadata "+hash" cannot be
//     used: npm drops it, so two contents would share one version.)
//   - Packing happens on a temporary copy; the user's folder is never
//     modified (no node_modules, no lockfile, no `npm pkg set`).
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/identity"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

// CommitPrefix marks local revisions.
const CommitPrefix = "local-"

// MaxFiles bounds the size of a registered folder (outside node_modules/.git).
var MaxFiles = 50000

// Source serves registered folders.
type Source struct {
	Index    *localindex.Store
	StateDir string // version counters: <consona home>/local

	mu    sync.Mutex
	memo  map[string]fileMemo // abs file path -> hash, keyed by size+mtime
	locks map[string]*sync.Mutex
}

type fileMemo struct {
	size  int64
	mtime time.Time
	mode  fs.FileMode
	sum   string
}

// New returns a Source over idx, keeping version counters under home.
func New(idx *localindex.Store, home string) *Source {
	return &Source{Index: idx, StateDir: filepath.Join(home, "local"), memo: map[string]fileMemo{}, locks: map[string]*sync.Mutex{}}
}

func name(ref core.Ref) string { return "@" + ref.Owner + "/" + ref.Name }

// Lookup returns the registered entry for ref.
func (s *Source) Lookup(ref core.Ref) (localindex.Entry, bool) {
	e, ok, _ := s.Index.Lookup(name(ref))
	return e, ok
}

// Live: local folders change without notice.
func (s *Source) Live(ref core.Ref) bool { _, ok := s.Lookup(ref); return ok }

func (s *Source) Describe(ref core.Ref) string {
	if e, ok := s.Lookup(ref); ok {
		return "local folder " + e.Path
	}
	return "local index (" + name(ref) + " not registered)"
}

// Current is the live state of a registered folder.
type Current struct {
	Entry   localindex.Entry
	Hash    string // full hex content hash
	Version string
}

// Resolve hashes the folder and returns its current version.
func (s *Source) Resolve(ref core.Ref) (*Current, error) {
	e, ok := s.Lookup(ref)
	if !ok {
		return nil, fmt.Errorf("%s: %w (not registered locally)", name(ref), core.ErrNotFound)
	}
	pj, err := os.ReadFile(filepath.Join(e.Path, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("%s is registered from %s, but that folder has no readable package.json any more (%v); fix it or `consona unregister %s`", e.Name, e.Path, err, e.Name)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pj, &m); err != nil {
		return nil, fmt.Errorf("%s: invalid package.json in %s: %v", e.Name, e.Path, err)
	}
	hash, err := s.hashTree(e.Path, true)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", e.Name, e.Path, err)
	}
	v, err := s.version(e.Name, m.Version, hash)
	if err != nil {
		return nil, err
	}
	return &Current{Entry: e, Hash: hash, Version: v}, nil
}

// Revisions returns the single live revision of the folder.
func (s *Source) Revisions(_ context.Context, ref core.Ref) ([]core.Revision, error) {
	c, err := s.Resolve(ref)
	if err != nil {
		return nil, err
	}
	return []core.Revision{{Version: c.Version, Commit: CommitPrefix + c.Hash[:32], Head: true}}, nil
}

// Fetch copies the folder (minus node_modules/.git) into dir and checks
// the copy still has the requested content hash.
func (s *Source) Fetch(_ context.Context, ref core.Ref, commit, dir string) error {
	e, ok := s.Lookup(ref)
	if !ok {
		return fmt.Errorf("%s: %w (not registered locally)", name(ref), core.ErrNotFound)
	}
	if err := CopyTree(e.Path, dir); err != nil {
		return fmt.Errorf("copying %s: %w", e.Path, err)
	}
	h, err := s.hashTree(dir, false)
	if err != nil {
		return err
	}
	if CommitPrefix+h[:32] != commit {
		return fmt.Errorf("%s changed while it was being packed (or this version is no longer its current content); run the install again", e.Path)
	}
	return nil
}

// MakeVersion builds "<base>-local.<n>.h<hash8>" ("." joins an existing
// prerelease). Build metadata is dropped; an invalid base becomes 0.0.0.
func MakeVersion(base string, n int, hash string) string {
	base, _, _ = strings.Cut(strings.TrimSpace(base), "+")
	base = strings.TrimPrefix(base, "v")
	if !core.ValidVersion(base) {
		base = "0.0.0"
	}
	sep := "-"
	if strings.Contains(base, "-") {
		sep = "."
	}
	return fmt.Sprintf("%s%slocal.%d.h%s", base, sep, n, hash[:8])
}

type verState struct {
	N       int    `json:"n"`
	Base    string `json:"base"`
	Hash    string `json:"hash"`
	Version string `json:"version"`
}

func (s *Source) lock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	return l
}

// version returns the version for (name, content); the counter goes up
// whenever the content or package.json version changes.
func (s *Source) version(pkg, base, hash string) (string, error) {
	scope, n, err := localindex.SplitName(strings.ToLower(pkg))
	if err != nil {
		return "", err
	}
	f := filepath.Join(s.StateDir, scope, n+".json")
	l := s.lock(f)
	l.Lock()
	defer l.Unlock()
	var st verState
	if b, err := os.ReadFile(f); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Hash == hash && st.Base == base && st.Version != "" {
		return st.Version, nil
	}
	unlock, err := identity.Lock(f)
	if err != nil {
		return "", err
	}
	defer unlock()
	if b, err := os.ReadFile(f); err == nil { // re-read under the file lock
		_ = json.Unmarshal(b, &st)
	}
	if st.Hash == hash && st.Base == base && st.Version != "" {
		return st.Version, nil
	}
	st = verState{N: st.N + 1, Base: base, Hash: hash}
	st.Version = MakeVersion(base, st.N, hash)
	b, _ := json.Marshal(st)
	return st.Version, identity.WriteFileAtomic(f, b, 0o600)
}

// skip reports whether a path (relative, slash-separated) is left out of
// the content hash and the build copy.
func skip(rel string, d fs.DirEntry) bool {
	base := d.Name()
	if d.IsDir() {
		return base == "node_modules" || base == ".git" || base == ".hg" || base == ".svn"
	}
	return base == ".DS_Store" || base == "Thumbs.db" || base == "npm-debug.log"
}

// walk visits regular files (symlinks and special files are ignored: they
// could point outside the folder).
func walk(root string, f func(rel string, path string, info fs.FileInfo) error) error {
	count := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return f(rel+"/", p, nil)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		count++
		if count > MaxFiles {
			return fmt.Errorf("more than %d files outside node_modules/.git; is this the right folder?", MaxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return f(rel, p, info)
	})
}

// HashTree is the content hash of a folder (exported for tests/tools).
func HashTree(root string) (string, error) {
	return (&Source{memo: map[string]fileMemo{}}).hashTree(root, false)
}

func (s *Source) hashTree(root string, memo bool) (string, error) {
	h := sha256.New()
	err := walk(root, func(rel, p string, info fs.FileInfo) error {
		if info == nil {
			fmt.Fprintf(h, "d %s\n", rel)
			return nil
		}
		sum, err := s.fileHash(p, info, memo)
		if err != nil {
			return err
		}
		x := "-"
		if info.Mode()&0o111 != 0 {
			x = "x"
		}
		fmt.Fprintf(h, "f %s %s %s\n", rel, x, sum)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileHash hashes one file, reusing the previous hash while size, mtime
// and mode are unchanged (memo=true, i.e. the user's folder).
func (s *Source) fileHash(p string, info fs.FileInfo, memo bool) (string, error) {
	if memo {
		s.mu.Lock()
		m, ok := s.memo[p]
		s.mu.Unlock()
		if ok && m.size == info.Size() && m.mtime.Equal(info.ModTime()) && m.mode == info.Mode() {
			return m.sum, nil
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if memo {
		s.mu.Lock()
		s.memo[p] = fileMemo{size: info.Size(), mtime: info.ModTime(), mode: info.Mode(), sum: sum}
		s.mu.Unlock()
	}
	return sum, nil
}

// CopyTree copies the hashed files of src into dst (which exists).
func CopyTree(src, dst string) error {
	return walk(src, func(rel, p string, info fs.FileInfo) error {
		target := filepath.Join(dst, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
		if info == nil {
			return os.MkdirAll(target, 0o755)
		}
		mode := fs.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// IsNotRegistered reports whether err is the "not registered" miss.
func IsNotRegistered(err error) bool { return errors.Is(err, core.ErrNotFound) }
