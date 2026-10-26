package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Token is one named token's metadata. The value itself is only in the
// OS secure store; nothing here is secret.
type Token struct {
	Name        string    `json:"name"`
	Owners      []string  `json:"owners,omitempty"` // --for owners (lowercase, no @)
	Description string    `json:"description,omitempty"`
	Default     bool      `json:"default,omitempty"`
	Type        string    `json:"type,omitempty"`  // classic, fine-grained, oauth, ...
	Login       string    `json:"login,omitempty"` // GitHub account the token acts as
	UserName    string    `json:"user_name,omitempty"`
	UserID      int64     `json:"user_id,omitempty"`
	Scopes      []string  `json:"scopes,omitempty"`  // classic tokens only
	Expires     string    `json:"expires,omitempty"` // RFC 3339, "" = none/unknown
	Added       time.Time `json:"added"`
	LastChecked time.Time `json:"last_checked,omitempty"`
}

// ExpiresAt parses Expires (zero if none).
func (t Token) ExpiresAt() time.Time {
	e, _ := time.Parse(time.RFC3339, t.Expires)
	return e
}

// For reports whether the token is mapped to owner.
func (t Token) For(owner string) bool {
	owner = NormOwner(owner)
	for _, o := range t.Owners {
		if o == owner {
			return true
		}
	}
	return false
}

// Index is the token index file: metadata only, no secrets.
type Index struct {
	Version int     `json:"version"`
	Tokens  []Token `json:"tokens"`
}

// ConfigDir is consona's config directory (see internal/config).
func ConfigDir() string { return config.Dir() }

// IndexPath is where the token index lives.
func IndexPath() string { return filepath.Join(ConfigDir(), "tokens.json") }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName checks a token name.
func ValidName(n string) error {
	if len(n) >= 20 && TokenType(n) != "unknown" {
		return fmt.Errorf("that looks like a token, not a name: the token is read from a hidden prompt or stdin (revoke it if it was real)")
	}
	if !nameRE.MatchString(n) {
		return fmt.Errorf("invalid token name %q: use 1-64 letters, digits, '.', '_' or '-' (it is a label, not the token)", n)
	}
	return nil
}

var ownerRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38})$`)

// NormOwner lowercases an owner and strips any leading '@'s.
func NormOwner(o string) string {
	return strings.ToLower(strings.TrimLeft(strings.TrimSpace(o), "@"))
}

// ValidOwner checks a GitHub user/org name (after NormOwner).
func ValidOwner(o string) error {
	if !ownerRE.MatchString(o) {
		return fmt.Errorf("invalid GitHub owner %q", o)
	}
	return nil
}

// LoadIndex reads the index; a missing file is an empty index.
func LoadIndex(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, fmt.Errorf("token index %s is not valid JSON: %v", path, err)
	}
	return &ix, nil
}

// Save writes the index atomically (temp file + rename), file mode 0600
// in a 0700 directory.
func (ix *Index) Save(path string) error {
	ix.Version = 1
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Find returns the token named n (case-sensitive), or nil.
func (ix *Index) Find(n string) *Token {
	for i := range ix.Tokens {
		if strings.EqualFold(ix.Tokens[i].Name, n) {
			return &ix.Tokens[i]
		}
	}
	return nil
}

// ErrExists is returned by Add for a duplicate name without replace.
var ErrExists = errors.New("a token with this name already exists")

// Add inserts t; an existing name is an error unless replace. A default
// token clears the default flag on the others.
func (ix *Index) Add(t Token, replace bool) error {
	if err := ValidName(t.Name); err != nil {
		return err
	}
	for i := range ix.Tokens {
		if strings.EqualFold(ix.Tokens[i].Name, t.Name) {
			if !replace {
				return fmt.Errorf("token %q: %w (use --replace to overwrite it, or `consona token remove %s`)", t.Name, ErrExists, ix.Tokens[i].Name)
			}
			ix.Tokens = append(ix.Tokens[:i], ix.Tokens[i+1:]...)
			break
		}
	}
	if t.Default {
		for i := range ix.Tokens {
			ix.Tokens[i].Default = false
		}
	}
	ix.Tokens = append(ix.Tokens, t)
	return nil
}

// Remove deletes the token named n; reports whether it existed.
func (ix *Index) Remove(n string) bool {
	for i := range ix.Tokens {
		if strings.EqualFold(ix.Tokens[i].Name, n) {
			ix.Tokens = append(ix.Tokens[:i], ix.Tokens[i+1:]...)
			return true
		}
	}
	return false
}

// Owners returns every --for owner across tokens.
func (ix *Index) Owners() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ix.Tokens {
		for _, o := range t.Owners {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}
