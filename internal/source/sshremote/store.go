package sshremote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/identity"
)

// Remote is a machine reachable over ssh.
type Remote struct {
	Name      string    `json:"name"`
	Target    string    `json:"target"` // user@host, host, or an ssh-config alias
	Port      int       `json:"port,omitempty"`
	Identity  string    `json:"identity,omitempty"`   // ssh -i
	RemoteCmd string    `json:"remote_cmd,omitempty"` // command that runs consona there; "remote-serve" is appended
	Added     time.Time `json:"added,omitempty"`
	Auto      bool      `json:"-"` // auto-detected from ssh config
}

// Key is a short stable id used in cache paths and revisions.
func (r Remote) Key() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%v", r.Name, r.Target, r.Port, r.Auto)))
	return hex.EncodeToString(h[:5])
}

// Label is "name (target)" or just the alias for auto-detected hosts.
func (r Remote) Label() string {
	if r.Auto {
		return r.Name + " (from ssh config)"
	}
	t := r.Target
	if r.Port != 0 {
		t += fmt.Sprintf(":%d", r.Port)
	}
	return r.Name + " (" + t + ")"
}

var remoteNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
var targetRe = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9._:%\[\]-]+$`)

// Validate checks the fields that reach the ssh command line.
func (r Remote) Validate() error {
	if !remoteNameRe.MatchString(r.Name) {
		return fmt.Errorf("invalid remote name %q: use lowercase letters, digits, '.', '_' or '-'", r.Name)
	}
	if strings.HasPrefix(r.Target, "-") || !targetRe.MatchString(r.Target) {
		return fmt.Errorf("invalid ssh target %q: want user@host, host, or an ssh config alias", r.Target)
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("invalid port %d", r.Port)
	}
	if strings.HasPrefix(r.Identity, "-") {
		return fmt.Errorf("invalid identity file %q", r.Identity)
	}
	return nil
}

// Store holds explicitly added remotes (remotes.json in the config dir).
type Store struct{ Dir string }

// OpenStore returns the store in consona's config dir.
func OpenStore() *Store { return &Store{Dir: identity.ConfigDir()} }

func (s *Store) path() string { return filepath.Join(s.Dir, "remotes.json") }

// List returns explicit remotes sorted by name.
func (s *Store) List() ([]Remote, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Remotes []Remote `json:"remotes"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", s.path(), err)
	}
	sort.Slice(f.Remotes, func(i, j int) bool { return f.Remotes[i].Name < f.Remotes[j].Name })
	return f.Remotes, nil
}

func (s *Store) write(rs []Remote) error {
	b, _ := json.MarshalIndent(map[string]any{"version": 1, "remotes": rs}, "", "  ")
	return identity.WriteFileAtomic(s.path(), append(b, '\n'), 0o600)
}

// Add saves r; an existing remote with the same name needs replace.
func (s *Store) Add(r Remote, replace bool) error {
	if err := r.Validate(); err != nil {
		return err
	}
	unlock, err := identity.Lock(s.path())
	if err != nil {
		return err
	}
	defer unlock()
	rs, err := s.List()
	if err != nil {
		return err
	}
	var keep []Remote
	for _, x := range rs {
		if x.Name == r.Name {
			if !replace {
				return fmt.Errorf("remote %q already exists (%s); use --replace to change it", r.Name, x.Label())
			}
			continue
		}
		keep = append(keep, x)
	}
	return s.write(append(keep, r))
}

// Remove deletes a remote by name.
func (s *Store) Remove(name string) error {
	unlock, err := identity.Lock(s.path())
	if err != nil {
		return err
	}
	defer unlock()
	rs, err := s.List()
	if err != nil {
		return err
	}
	var keep []Remote
	found := false
	for _, x := range rs {
		if x.Name == name {
			found = true
			continue
		}
		keep = append(keep, x)
	}
	if !found {
		return fmt.Errorf("no remote named %q (see `consona remote list`)", name)
	}
	return s.write(keep)
}
