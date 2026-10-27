package creds

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Every account gh is logged into for github.com is a credential source.
// consona only reads gh (hosts.yml, `gh auth status`, `gh auth token
// --user`): it never switches gh's active account or writes gh's config.

// GHAccount is one gh login for github.com.
type GHAccount struct {
	Login  string
	Active bool
	Token  string // never print it (use redact.Mask)
	Err    error  // why the token could not be read
}

// GHRunner runs gh with args (and extra env) and returns its stdout and
// stderr. Tests inject a fake.
type GHRunner func(env []string, args ...string) (stdout, stderr string, err error)

// ErrGHNoUserFlag means the installed gh predates `gh auth token --user`
// (gh < 2.40): consona then uses the single gh login, as before.
var ErrGHNoUserFlag = errors.New("this gh has no `gh auth token --user` (gh < 2.40): only the active gh account is used")

// ExecGH runs the real gh, without GH_TOKEN/GITHUB_TOKEN (those env vars
// are their own sources).
func ExecGH(env []string, args ...string) (string, string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", "", fmt.Errorf("gh is not installed (optional)")
	}
	cmd := exec.Command("gh", args...)
	cmd.Env = append(append(os.Environ(), "GH_TOKEN=", "GITHUB_TOKEN=", "GH_PROMPT_DISABLED=1", "NO_COLOR=1"), env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// SystemGHAccounts is DiscoverGH with the installed gh and the process
// environment.
func SystemGHAccounts() ([]GHAccount, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh is not installed (optional)")
	}
	return DiscoverGH(ExecGH, os.Getenv)
}

// GHConfigDir is the directory gh reads hosts.yml from.
func GHConfigDir(getenv func(string) string) string {
	if d := getenv("GH_CONFIG_DIR"); d != "" {
		return d
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh")
	}
	if runtime.GOOS == "windows" {
		if d := getenv("AppData"); d != "" {
			return filepath.Join(d, "GitHub CLI")
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "gh")
}

// ParseHostsYML returns the github.com logins in a gh hosts.yml and the
// active one. Multi-account gh (>= 2.40) lists them under `users:` and
// names the active one in `user:`; older gh has only `user:`.
func ParseHostsYML(b []byte) (logins []string, active string) {
	in, inUsers := false, false
	usersIndent, childIndent := -1, -1
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		trim := strings.TrimLeft(line, " ")
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		ind := len(line) - len(trim)
		if ind == 0 {
			in = strings.Trim(strings.TrimSuffix(trim, ":"), `"'`) == "github.com"
			inUsers = false
			continue
		}
		if !in {
			continue
		}
		k, v, _ := strings.Cut(trim, ":")
		k, v = strings.Trim(strings.TrimSpace(k), `"'`), strings.Trim(strings.TrimSpace(v), `"'`)
		if inUsers && ind > usersIndent {
			if childIndent < 0 {
				childIndent = ind
			}
			if ind == childIndent && k != "" {
				logins = append(logins, k)
			}
			continue
		}
		inUsers = false
		switch k {
		case "users":
			inUsers, usersIndent, childIndent = true, ind, -1
		case "user":
			active = v
		}
	}
	if active != "" {
		found := false
		for _, l := range logins {
			found = found || strings.EqualFold(l, active)
		}
		if !found {
			logins = append([]string{active}, logins...)
		}
	}
	return logins, active
}

var (
	statusAccountRE = regexp.MustCompile(`Logged in to github\.com (?:account|as) ([A-Za-z0-9-]+)`)
	statusActiveRE  = regexp.MustCompile(`Active account:\s*(true|false)`)
)

// ParseAuthStatus reads `gh auth status --hostname github.com` output
// (gh writes it to stdout or stderr depending on the version).
func ParseAuthStatus(s string) (logins []string, active string) {
	last := ""
	for _, line := range strings.Split(s, "\n") {
		if m := statusAccountRE.FindStringSubmatch(line); m != nil {
			last = m[1]
			logins = append(logins, last)
			continue
		}
		if m := statusActiveRE.FindStringSubmatch(line); m != nil && m[1] == "true" && last != "" {
			active = last
		}
	}
	if active == "" && len(logins) == 1 { // old gh: one account, no "Active account" line
		active = logins[0]
	}
	return logins, active
}

// DiscoverGH lists every github.com account gh is logged into, the
// active one first, each with its token from `gh auth token --hostname
// github.com --user <login>`. ErrGHNoUserFlag (with the logins found)
// means the caller should fall back to the single gh login.
func DiscoverGH(run GHRunner, getenv func(string) string) ([]GHAccount, error) {
	if dir := getenv("GH_CONFIG_DIR"); dir != "" {
		if err := ghConfigDirLoggedIn(dir); err != nil {
			return nil, err
		}
	}
	var logins []string
	var active string
	if b, err := os.ReadFile(filepath.Join(GHConfigDir(getenv), "hosts.yml")); err == nil {
		logins, active = ParseHostsYML(b)
	}
	if len(logins) == 0 { // e.g. a keyring-only setup gh describes but hosts.yml does not
		out, errOut, _ := run(nil, "auth", "status", "--hostname", "github.com")
		logins, active = ParseAuthStatus(out + "\n" + errOut)
	}
	if len(logins) == 0 {
		return nil, fmt.Errorf("gh is installed but not logged in (`gh auth login`)")
	}
	sort.SliceStable(logins, func(i, j int) bool {
		return strings.EqualFold(logins[i], active) && !strings.EqualFold(logins[j], active)
	})
	accts := make([]GHAccount, 0, len(logins))
	for _, l := range logins {
		a := GHAccount{Login: l, Active: strings.EqualFold(l, active)}
		out, errOut, err := run(nil, "auth", "token", "--hostname", "github.com", "--user", l)
		switch tok := strings.TrimSpace(out); {
		case err == nil && tok != "":
			a.Token = tok
		case err != nil && strings.Contains(strings.ToLower(errOut), "unknown"): // unknown flag --user / unknown command "token"
			return accts, ErrGHNoUserFlag
		case err != nil && strings.TrimSpace(errOut) == "" && strings.Contains(err.Error(), "not installed"):
			return nil, err
		default:
			msg := strings.TrimSpace(errOut)
			if msg == "" && err != nil {
				msg = err.Error()
			}
			if msg == "" {
				msg = "no token"
			}
			a.Err = fmt.Errorf("gh auth token --user %s: %s", l, firstLine(msg))
		}
		accts = append(accts, a)
	}
	return accts, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// GHMap persists which gh account can read an owner that is not itself a
// gh login (an organisation or another user), in consona's config dir.
type GHMap struct {
	Path string
	mu   sync.Mutex
}

type ghMapFile struct {
	Owners map[string]string `json:"owners"` // owner (lower) -> gh login
}

func (m *GHMap) load() ghMapFile {
	f := ghMapFile{}
	if m == nil || m.Path == "" {
		return f
	}
	if b, err := os.ReadFile(m.Path); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	if f.Owners == nil {
		f.Owners = map[string]string{}
	}
	return f
}

// Get is the gh login remembered for owner ("" if none).
func (m *GHMap) Get(owner string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load().Owners[NormOwner(owner)]
}

// Set remembers login for owner; login "" forgets it.
func (m *GHMap) Set(owner, login string) error {
	if m == nil || m.Path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.load()
	owner = NormOwner(owner)
	if f.Owners[owner] == login || (login == "" && f.Owners[owner] == "") {
		return nil
	}
	if login == "" {
		delete(f.Owners, owner)
	} else {
		f.Owners[owner] = login
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o700); err != nil {
		return err
	}
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}

// GHMapPath is where the owner -> gh account mapping lives.
func GHMapPath() string { return filepath.Join(ConfigDir(), "gh-accounts.json") }
