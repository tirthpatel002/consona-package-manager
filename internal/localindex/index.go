// Package localindex is the device's index of registered local package
// folders. Every entry is unique by package name and by canonical folder
// path, so a device never has two packages for one folder or two folders
// for one name. The index file lives in consona's config dir.
package localindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/identity"
)

// Entry is one registered folder.
type Entry struct {
	Name       string    `json:"name"`      // npm name, always "@scope/name"
	Path       string    `json:"path"`      // canonical absolute folder path
	DeviceID   string    `json:"device_id"` // device that registered it
	Registered time.Time `json:"registered"`
}

// Scope returns the entry's scope without "@".
func (e Entry) Scope() string { s, _, _ := SplitName(e.Name); return s }

type file struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Store is the index file in Dir.
type Store struct{ Dir string }

// Open returns the store in consona's config dir.
func Open() *Store { return &Store{Dir: identity.ConfigDir()} }

func (s *Store) path() string { return filepath.Join(s.Dir, "local-index.json") }

// List returns all entries sorted by name. A missing index is empty.
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

// Scopes returns the scopes (lowercase, no "@") of all entries.
func (s *Store) Scopes() map[string]bool {
	es, _ := s.List()
	out := map[string]bool{}
	for _, e := range es {
		out[strings.ToLower(e.Scope())] = true
	}
	return out
}

// ConflictError is a registration that would duplicate a name or folder.
type ConflictError struct {
	Kind     string // "name", "path" or "same"
	Existing Entry
	Name     string
	Path     string
}

func (c *ConflictError) Error() string {
	switch c.Kind {
	case "same":
		return fmt.Sprintf("%s is already registered from %s (use --replace to refresh the entry)", c.Name, c.Path)
	case "name":
		return fmt.Sprintf("%s is already registered from another folder: %s (one name, one folder per device; use --replace to point it at %s, or pick another --name)", c.Name, c.Existing.Path, c.Path)
	default:
		return fmt.Sprintf("folder %s is already registered as %s (one folder, one name per device; use --replace to rename it to %s, or `consona unregister %s` first)", c.Path, c.Existing.Name, c.Name, c.Existing.Name)
	}
}

// Add registers e (Path must already be canonical). Without replace any
// entry with the same name or the same folder is a *ConflictError; with
// replace those entries are removed and returned.
func (s *Store) Add(e Entry, replace bool) (removed []Entry, err error) {
	e.Name = strings.ToLower(e.Name) // npm names are lowercase
	if _, _, err := SplitName(e.Name); err != nil {
		return nil, err
	}
	unlock, err := identity.Lock(s.path())
	if err != nil {
		return nil, err
	}
	defer unlock()
	es, err := s.List()
	if err != nil {
		return nil, err
	}
	var keep []Entry
	for _, x := range es {
		sameName, samePath := strings.EqualFold(x.Name, e.Name), SamePath(x.Path, e.Path)
		if !sameName && !samePath {
			keep = append(keep, x)
			continue
		}
		if !replace {
			kind := "path"
			switch {
			case sameName && samePath:
				kind = "same"
			case sameName:
				kind = "name"
			}
			return nil, &ConflictError{Kind: kind, Existing: x, Name: e.Name, Path: e.Path}
		}
		removed = append(removed, x)
	}
	keep = append(keep, e)
	return removed, s.write(keep)
}

// Remove unregisters name.
func (s *Store) Remove(name string) (Entry, error) {
	unlock, err := identity.Lock(s.path())
	if err != nil {
		return Entry{}, err
	}
	defer unlock()
	es, err := s.List()
	if err != nil {
		return Entry{}, err
	}
	var keep []Entry
	var gone *Entry
	for i, x := range es {
		if strings.EqualFold(x.Name, name) {
			gone = &es[i]
			continue
		}
		keep = append(keep, x)
	}
	if gone == nil {
		return Entry{}, fmt.Errorf("%s is not registered on this device (see `consona registered`)", name)
	}
	return *gone, s.write(keep)
}

func (s *Store) write(es []Entry) error {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	b, _ := json.MarshalIndent(file{Version: 1, Entries: es}, "", "  ")
	return identity.WriteFileAtomic(s.path(), append(b, '\n'), 0o600)
}

// Canonical returns the absolute, symlink-resolved, cleaned form of p.
func Canonical(p string) (string, error) {
	if strings.HasPrefix(p, "~/") || p == "~" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// SamePath compares canonical paths; case-insensitively on macOS and
// Windows, whose default file systems are case-insensitive.
func SamePath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

var partRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._~-]*$`)

// SplitName validates an npm scoped name "@scope/name" and splits it.
func SplitName(name string) (scope, pkg string, err error) {
	bad := fmt.Errorf("invalid package name %q: want @scope/name in lowercase (letters, digits, '.', '_', '~', '-')", name)
	if !strings.HasPrefix(name, "@") || len(name) > 214 {
		return "", "", bad
	}
	scope, pkg, ok := strings.Cut(name[1:], "/")
	if !ok || !partRe.MatchString(scope) || !partRe.MatchString(pkg) {
		return "", "", bad
	}
	return scope, pkg, nil
}

// DefaultName turns a package.json name into "@username/<name>": the
// scope is forced to the user's own.
func DefaultName(pkgName, username string) (string, error) {
	n := strings.TrimSpace(pkgName)
	if strings.HasPrefix(n, "@") {
		if _, rest, ok := strings.Cut(n, "/"); ok {
			n = rest
		}
	}
	if n == "" {
		return "", fmt.Errorf("package.json has no \"name\"; pass --name @%s/<name>", username)
	}
	name := "@" + username + "/" + strings.ToLower(n)
	_, _, err := SplitName(name)
	return name, err
}
