// Package identity holds this device's consona identity: a free local
// username (not tied to GitHub) and a device record created once. It lives
// in consona's config dir (0700), in files written atomically with 0600.
package identity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ErrNotInitialized means `consona init` has not been run on this device.
var ErrNotInitialized = errors.New("consona is not initialized on this device: run `consona init --username <name>` once")

// Device describes this machine. ID is a random UUID generated once.
type Device struct {
	ID       string    `json:"id"`
	Hostname string    `json:"hostname"`
	OS       string    `json:"os"`
	Arch     string    `json:"arch"`
	Created  time.Time `json:"created"`
}

// Short is "<hostname> (<first 8 of id>)".
func (d Device) Short() string {
	id := d.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return d.Hostname + " (" + id + ")"
}

// Identity is the username plus this device's record.
type Identity struct {
	Username string    `json:"username"`
	Device   Device    `json:"device"`
	Updated  time.Time `json:"updated"`
}

// ConfigDir is consona's config directory (see internal/config).
func ConfigDir() string { return config.Dir() }

// Path is the identity file.
func Path() string { return filepath.Join(ConfigDir(), "identity.json") }

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

// NormalizeUsername lowercases and strips a leading "@"; it fails unless
// the result is usable as an npm scope (letters, digits, ".", "_", "-";
// starting with a letter or digit; at most 39 characters).
func NormalizeUsername(s string) (string, error) {
	u := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	if !usernameRe.MatchString(u) {
		return "", fmt.Errorf("invalid username %q: use 1-39 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit (it becomes your npm scope @<username>)", s)
	}
	return u, nil
}

// Load reads the identity; ErrNotInitialized if there is none.
func Load() (*Identity, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", Path(), err)
	}
	if id.Username == "" || id.Device.ID == "" {
		return nil, ErrNotInitialized
	}
	return &id, nil
}

// Init creates the identity (username + new device record). Re-running
// with the same username is a no-op (created=false). A different username
// needs force; the device record is always kept.
func Init(username string, force bool) (id *Identity, created bool, err error) {
	u, err := NormalizeUsername(username)
	if err != nil {
		return nil, false, err
	}
	old, err := Load()
	switch {
	case err == nil && old.Username == u:
		return old, false, nil
	case err == nil && !force:
		return old, false, fmt.Errorf("already initialized as %q on this device (%s); use `consona init --username %s --force` to change the username (the device id is kept)", old.Username, Path(), u)
	case err != nil && !errors.Is(err, ErrNotInitialized):
		return nil, false, err
	}
	now := time.Now()
	if old != nil {
		old.Username, old.Updated = u, now
		id = old
	} else {
		host, _ := os.Hostname()
		id = &Identity{Username: u, Updated: now, Device: Device{ID: NewUUID(), Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Created: now}}
	}
	b, _ := json.MarshalIndent(id, "", "  ")
	if err := WriteFileAtomic(Path(), append(b, '\n'), 0o600); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

// NewUUID returns a random (version 4) UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// EnsureDir creates dir (and parents) and makes dir itself 0700.
func EnsureDir(dir string) error { return config.EnsureDir(dir) }

// WriteFileAtomic writes data atomically (see config.WriteFileAtomic).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return config.WriteFileAtomic(path, data, perm)
}

// Lock takes an advisory lock file next to path (created exclusively),
// waiting up to 5s; a lock older than 30s is considered stale. The
// returned func releases it.
func Lock(path string) (func(), error) {
	lp := path + ".lock"
	if err := EnsureDir(filepath.Dir(lp)); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(lp) }, nil
		}
		if st, serr := os.Stat(lp); serr == nil && time.Since(st.ModTime()) > 30*time.Second {
			os.Remove(lp)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked by another consona process (remove %s if no consona command is running)", path, lp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
