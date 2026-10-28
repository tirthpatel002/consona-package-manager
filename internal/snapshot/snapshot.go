// Package snapshot records a project's frozen resolution inputs: for each
// GitHub repo the default-branch head and the tag->commit map, captured
// once. A snapshot is content-addressed (SHA-256 of its file) and reaches
// the daemon inside the request URL (/npm/s/<hash>/...), never through
// shared daemon state, so concurrent projects cannot mix.
//
// What it guarantees: every package of an install is resolved against the
// same recorded tag set and head per repo. It does not make independently
// released packages share one commit, and it does not make their semver
// ranges compatible; a conflict is reproducible, not timing-dependent.
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

const (
	// FileName is the project file `consona snapshot create` writes.
	FileName = "consona.snapshot.json"
	// MaxBytes bounds every snapshot file read.
	MaxBytes = 256 << 10
	// SchemaVersion is the only file version this build understands.
	SchemaVersion = 1

	maxRepos   = 200
	maxTags    = 5000
	maxScopes  = 100
	maxTagName = 200
)

// Repo is one repo's frozen state.
type Repo struct {
	Head      string            `json:"head"`               // default-branch head commit
	Tags      map[string]string `json:"tags"`               // tag name -> commit
	CaptureAt time.Time         `json:"captured,omitempty"` // when the stable read finished
}

// Folder is a package's recorded folder-map entry. A snapshot request
// fails when the device's current map differs, so a remapped name can never
// serve another folder's code under frozen commits.
type Folder struct {
	Repo string `json:"repo"` // lowercase "owner/repo"
	Path string `json:"path"` // clean slash-separated folder
}

// Snapshot is the parsed file.
type Snapshot struct {
	Version int               `json:"version"`
	Scopes  []string          `json:"scopes"`            // npm scopes (without @) routed through this snapshot
	Repos   map[string]Repo   `json:"repos"`             // lowercase "owner/repo"
	Folders map[string]Folder `json:"folders,omitempty"` // lowercase "@scope/name" -> mapped folder
}

var (
	hashRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoRe   = regexp.MustCompile(`^[a-z0-9_.-]{1,100}/[a-z0-9_.-]{1,100}$`)
	scopeRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,99}$`)
	pkgRe    = regexp.MustCompile(`^@[a-z0-9][a-z0-9_.-]{0,99}/[a-z0-9][a-z0-9_.-]{0,99}$`)
	pathRe   = regexp.MustCompile(`^[A-Za-z0-9_.@+=-]+(/[A-Za-z0-9_.@+=-]+)*$`)
)

// ValidHash reports whether h is a snapshot hash (64 lowercase hex).
func ValidHash(h string) bool { return hashRe.MatchString(h) }

// Hash is the content address of file bytes.
func Hash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// ErrNotInSnapshot means a request under a snapshot named a repo the
// snapshot does not record. It deliberately is not core.ErrNotFound: no
// source may fall through to a live lookup because of it.
var ErrNotInSnapshot = errors.New("repo is not in the project's consona snapshot")

// ErrRefused marks a request the snapshot cannot serve consistently (a live
// local folder, a changed or unrecorded folder mapping). The registry
// answers it with 409 so npm does not retry it.
var ErrRefused = errors.New("not allowed under the project's consona snapshot")

// Parse validates snapshot file bytes strictly: size, unknown fields,
// repo/scope/commit/tag shapes and counts.
func Parse(data []byte) (*Snapshot, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("snapshot is larger than %d bytes", MaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Snapshot
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid snapshot: %v", err)
	}
	if dec.More() {
		return nil, errors.New("invalid snapshot: trailing data")
	}
	if s.Version != SchemaVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d (this consona reads version %d)", s.Version, SchemaVersion)
	}
	if len(s.Repos) == 0 || len(s.Repos) > maxRepos {
		return nil, fmt.Errorf("snapshot must record 1..%d repos", maxRepos)
	}
	if len(s.Scopes) == 0 || len(s.Scopes) > maxScopes {
		return nil, fmt.Errorf("snapshot must list 1..%d scopes", maxScopes)
	}
	for _, sc := range s.Scopes {
		if !scopeRe.MatchString(sc) {
			return nil, fmt.Errorf("invalid scope %q (lowercase, no @)", sc)
		}
	}
	for name, r := range s.Repos {
		if o, n, _ := strings.Cut(name, "/"); !repoRe.MatchString(name) || o == "." || o == ".." || n == "." || n == ".." {
			return nil, fmt.Errorf("invalid repo %q: want lowercase owner/repo", name)
		}
		if !commitRe.MatchString(r.Head) {
			return nil, fmt.Errorf("repo %s: head %q is not a 40-hex commit", name, r.Head)
		}
		if len(r.Tags) > maxTags {
			return nil, fmt.Errorf("repo %s: more than %d tags", name, maxTags)
		}
		for t, c := range r.Tags {
			if t == "" || len(t) > maxTagName || strings.ContainsAny(t, " \t\r\n\x00") || strings.HasPrefix(t, "-") {
				return nil, fmt.Errorf("repo %s: invalid tag name %q", name, t)
			}
			if !commitRe.MatchString(c) {
				return nil, fmt.Errorf("repo %s: tag %s points at %q, not a 40-hex commit", name, t, c)
			}
		}
	}
	if len(s.Folders) > maxRepos*10 {
		return nil, errors.New("too many mapped folders")
	}
	for name, f := range s.Folders {
		if !pkgRe.MatchString(name) {
			return nil, fmt.Errorf("invalid mapped package name %q", name)
		}
		if _, ok := s.Repos[f.Repo]; !ok {
			return nil, fmt.Errorf("folder %s maps to repo %q, which the snapshot does not record", name, f.Repo)
		}
		if len(f.Path) > 300 || !pathRe.MatchString(f.Path) || hasDotSegment(f.Path) {
			return nil, fmt.Errorf("folder %s: invalid path %q", name, f.Path)
		}
	}
	return &s, nil
}

func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// Marshal renders a snapshot deterministically (map keys sorted by encoding/json).
func Marshal(s *Snapshot) ([]byte, error) {
	sort.Strings(s.Scopes)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// TagsAndHead returns the recorded tags (sorted by name, so duplicate
// spellings such as v1.0.0 and 1.0.0 resolve the same way every time) and
// head for r. A repo the snapshot does not record is an error.
func (s *Snapshot) TagsAndHead(r core.Ref) ([]core.Tag, string, error) {
	rec, ok := s.Repos[strings.ToLower(r.Owner+"/"+r.Name)]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s/%s (run `consona snapshot update` to add it)", ErrNotInSnapshot, r.Owner, r.Name)
	}
	names := make([]string, 0, len(rec.Tags))
	for t := range rec.Tags {
		names = append(names, t)
	}
	sort.Strings(names)
	tags := make([]core.Tag, 0, len(names))
	for _, t := range names {
		tags = append(tags, core.Tag{Name: t, Commit: rec.Tags[t]})
	}
	return tags, rec.Head, nil
}

// Dir is where snapshots are stored by hash.
func Dir() string { return filepath.Join(config.Dir(), "snapshots") }

// Save validates data and stores it under its hash (atomic, mode 0600).
func Save(data []byte) (string, error) {
	if _, err := Parse(data); err != nil {
		return "", err
	}
	h := Hash(data)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(Dir(), ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(Dir(), h+".json")); err != nil {
		return "", err
	}
	return h, nil
}

// Load reads the stored snapshot for hash and re-verifies it: the hash is
// validated before it becomes part of a path, the bytes must hash to it,
// and the content must parse.
func Load(hash string) (*Snapshot, error) {
	if !ValidHash(hash) {
		return nil, errors.New("invalid snapshot id")
	}
	data, err := readCapped(filepath.Join(Dir(), hash+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("snapshot %.12s is not loaded on this device: run `consona snapshot use`", hash)
		}
		return nil, err
	}
	if Hash(data) != hash {
		return nil, fmt.Errorf("stored snapshot %.12s is corrupt (hash mismatch): run `consona snapshot use` again", hash)
	}
	return Parse(data)
}

// ReadFile reads a user-supplied snapshot file: it must be a regular file
// (a symlink is refused) no larger than MaxBytes.
func ReadFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (symlinks are not followed)", path)
	}
	return readCapped(path)
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxBytes)
	}
	return b, nil
}

type ctxKey struct{}

type ctxVal struct {
	hash string
	snap *Snapshot
}

// With returns ctx carrying snap for this request only, and its id for the
// engine's version memo.
func With(ctx context.Context, hash string, snap *Snapshot) context.Context {
	ctx = core.WithSnapshotID(ctx, hash)
	return context.WithValue(ctx, ctxKey{}, ctxVal{hash, snap})
}

// FromContext returns the request's snapshot, if any.
func FromContext(ctx context.Context) (*Snapshot, string, bool) {
	v, ok := ctx.Value(ctxKey{}).(ctxVal)
	return v.snap, v.hash, ok
}

// Fetcher reads a repo's raw tags and default-branch head live.
type Fetcher func(ctx context.Context) ([]core.Tag, string, error)

// Capture reads a repo until two consecutive reads agree (head and every
// tag), retrying up to attempts times. The result means "observed stable
// across the capture window", not an atomic GitHub state: GitHub offers no
// atomic multi-ref read, and a change between the two reads is detected
// only if it is still in place at the second read.
func Capture(ctx context.Context, fetch Fetcher, attempts int, wait func(int)) (Repo, error) {
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		a, ha, err := fetch(ctx)
		if err != nil {
			return Repo{}, err
		}
		b, hb, err := fetch(ctx)
		if err != nil {
			return Repo{}, err
		}
		if ha == hb && sameTags(a, b) {
			if !commitRe.MatchString(ha) {
				return Repo{}, fmt.Errorf("default-branch head %q is not a 40-hex commit", ha)
			}
			r := Repo{Head: ha, Tags: map[string]string{}, CaptureAt: time.Now().UTC().Truncate(time.Second)}
			for _, t := range a {
				if t.Commit == "" {
					continue
				}
				r.Tags[t.Name] = t.Commit
			}
			return r, nil
		}
		if wait != nil {
			wait(i + 1)
		}
	}
	return Repo{}, errors.New("repo changed during every capture attempt; try again when it is quiet")
}

func sameTags(a, b []core.Tag) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]string{}
	for _, t := range a {
		m[t.Name] = t.Commit
	}
	for _, t := range b {
		if c, ok := m[t.Name]; !ok || c != t.Commit {
			return false
		}
	}
	return true
}

// FromContextOK reports whether ctx resolves under a snapshot.
func FromContextOK(ctx context.Context) bool { _, _, ok := FromContext(ctx); return ok }
