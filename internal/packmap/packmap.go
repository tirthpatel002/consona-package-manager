// Package packmap is the device's map of package names to folders inside
// GitHub repos: "@scope/pkg" -> owner/repo + a folder in it. It lets one
// private repo (a monorepo) serve many packages without being split and
// without publishing (see internal/source/ghfolder). The map file lives
// in consona's config dir and is written atomically with mode 0600, like
// the other config files.
package packmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

// File is the map file name inside consona's config dir.
const File = "folder-map.json"

// Entry maps one package name to a folder inside a GitHub repo.
type Entry struct {
	Name   string    `json:"name"`             // npm name, always "@scope/name"
	Repo   string    `json:"repo"`             // "owner/repo" on GitHub
	Path   string    `json:"path"`             // folder inside the repo, slash-separated
	Synced bool      `json:"synced,omitempty"` // declared by the repo's own map file (consona map sync)
	Mapped time.Time `json:"mapped"`
}

// RepoRef returns the entry's repo as owner and name.
func (e Entry) RepoRef() (owner, repo string) {
	owner, repo, _ = strings.Cut(e.Repo, "/")
	return owner, repo
}

type file struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Store is the map file in Dir.
type Store struct{ Dir string }

// Open returns the store in consona's config dir.
func Open() *Store { return &Store{Dir: config.Dir()} }

func (s *Store) path() string { return filepath.Join(s.Dir, File) }

// Normalize validates an entry and normalizes its fields: the name is
// lowercased, the repo is "owner/repo", and the path becomes a clean
// slash-separated folder inside the repo (no ".", ".." or leading slash).
func Normalize(e *Entry) error {
	if err := normalizeNamePath(e); err != nil {
		return err
	}
	e.Repo = strings.TrimSpace(e.Repo)
	owner, repo, ok := strings.Cut(e.Repo, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || owner == ".." || repo == ".." ||
		strings.ContainsAny(owner+repo, " \t\n") {
		return fmt.Errorf("invalid repo %q: want owner/repo", e.Repo)
	}
	return nil
}

// normalizeNamePath validates the name and folder of an entry whose repo
// is set later (the repo's own map file does not name its repo).
func normalizeNamePath(e *Entry) error {
	e.Name = strings.ToLower(strings.TrimSpace(e.Name))
	if _, _, err := localindex.SplitName(e.Name); err != nil {
		return err
	}
	p := path.Clean(strings.ReplaceAll(strings.TrimSpace(e.Path), "\\", "/"))
	if p == "" || p == "." || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("invalid folder %q: want a path inside the repo, such as packages/<name>", e.Path)
	}
	e.Path = p
	return nil
}

// List returns all entries sorted by name. A missing map is empty.
func (s *Store) List() ([]Entry, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", s.path(), err)
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].Name < f.Entries[j].Name })
	return f.Entries, nil
}

func (s *Store) write(entries []Entry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	bs, err := json.MarshalIndent(file{Version: 1, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path(), append(bs, '\n'), 0o600)
}

// Lookup returns the entry for name (case-insensitive).
func (s *Store) Lookup(name string) (Entry, bool, error) {
	es, err := s.List()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range es {
		if strings.EqualFold(e.Name, name) {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Add inserts or replaces the entry for e.Name.
func (s *Store) Add(e Entry) error {
	if err := Normalize(&e); err != nil {
		return err
	}
	if e.Mapped.IsZero() {
		e.Mapped = time.Now()
	}
	es, err := s.List()
	if err != nil {
		return err
	}
	out := es[:0]
	for _, x := range es {
		if !strings.EqualFold(x.Name, e.Name) {
			out = append(out, x)
		}
	}
	return s.write(append(out, e))
}

// Remove drops the entry for name; it fails when the name is not mapped.
func (s *Store) Remove(name string) error {
	es, err := s.List()
	if err != nil {
		return err
	}
	out := es[:0]
	found := false
	for _, x := range es {
		if strings.EqualFold(x.Name, name) {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return fmt.Errorf("%s is not mapped", name)
	}
	return s.write(out)
}

// Sync replaces the synced entries of repo with entries (each forced to
// that repo and marked Synced): folders the repo no longer declares stop
// being served, and an entry it declares replaces any entry for the same
// name. Manual entries for other repos are kept. It returns how many
// entries the repo now declares and how many stale ones were removed.
func (s *Store) Sync(repo string, entries []Entry) (declared, removed int, err error) {
	seen := map[string]bool{}
	for i := range entries {
		entries[i].Repo = repo
		entries[i].Synced = true
		if err := Normalize(&entries[i]); err != nil {
			return 0, 0, fmt.Errorf("entry %d: %w", i+1, err)
		}
		if seen[entries[i].Name] {
			return 0, 0, fmt.Errorf("%s is declared twice", entries[i].Name)
		}
		seen[entries[i].Name] = true
		if entries[i].Mapped.IsZero() {
			entries[i].Mapped = time.Now()
		}
	}
	es, err := s.List()
	if err != nil {
		return 0, 0, err
	}
	out := es[:0]
	for _, x := range es {
		switch {
		case x.Synced && strings.EqualFold(x.Repo, repo) && !seen[strings.ToLower(x.Name)]:
			removed++ // the repo no longer declares it
		case seen[strings.ToLower(x.Name)]:
			continue // replaced by the repo's declaration
		default:
			out = append(out, x)
		}
	}
	out = append(out, entries...)
	return len(entries), removed, s.write(out)
}

// Scopes returns the mapped entries' scopes (without "@").
func (s *Store) Scopes() map[string]bool {
	out := map[string]bool{}
	if es, err := s.List(); err == nil {
		for _, e := range es {
			if sc, _, err := localindex.SplitName(e.Name); err == nil {
				out[sc] = true
			}
		}
	}
	return out
}

// MapFile is the repo-root file `consona map sync` reads
// (consona.packages.json): one repo keeps the list of the packages it
// serves, so every consumer maps them all with one command instead of
// adding each by hand.
type MapFile struct {
	Packages []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"packages"`
}

// ParseMapFile parses and validates a repo's consona.packages.json.
func ParseMapFile(bs []byte) ([]Entry, error) {
	var mf MapFile
	if err := json.Unmarshal(bs, &mf); err != nil {
		return nil, fmt.Errorf("invalid consona.packages.json: %w", err)
	}
	if len(mf.Packages) == 0 {
		return nil, errors.New("consona.packages.json declares no packages")
	}
	out := make([]Entry, len(mf.Packages))
	for i, p := range mf.Packages {
		out[i] = Entry{Name: p.Name, Path: p.Path, Synced: true}
		if err := normalizeNamePath(&out[i]); err != nil {
			return nil, fmt.Errorf("entry %d (%s): %w", i+1, p.Name, err)
		}
	}
	return out, nil
}
