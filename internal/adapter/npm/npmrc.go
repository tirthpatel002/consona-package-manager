package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// The user npmrc gets one consona block. npm rewrites the file without
// comments whenever it saves config (npm login, npm config set), so
// nothing here depends on the block markers: consona lines are recognised
// by their values (they point at the daemon), and the previous registry is
// kept in consona's state file, not in the npmrc.

const (
	blockStart = "# >>> consona: npm asks the local consona daemon first (managed by `consona setup`, undo: `consona setup --undo`) >>>"
	blockEnd   = "# <<< consona <<<"
	// DefaultUpstream is npm's default registry.
	DefaultUpstream = "https://registry.npmjs.org/"
	// Placeholder is the "token" npm holds for the daemon. It is not a
	// secret: it tells the daemon to use the user's own login for the
	// upstream registry, read from the npmrc at request time.
	Placeholder = "consona-uses-your-registry-login"
)

// retryLines make npm fail in ~2s instead of ~70s if the daemon is down,
// while keeping npm's 2 retries (which also cover a daemon restart).
var retryLines = []string{"fetch-retry-mintimeout=500", "fetch-retry-maxtimeout=2000"}

// UserConfig is the npm user config path npm itself would use.
func UserConfig() string {
	for _, k := range []string{"npm_config_userconfig", "NPM_CONFIG_USERCONFIG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".npmrc")
}

type state struct {
	Upstream     string `json:"upstream"`      // registry public requests go to
	RegistryLine string `json:"registry_line"` // user's own registry= line, restored on undo
	// LoglevelAdded is true when consona wrote loglevelLine (the user had
	// no loglevel of their own). npm strips comments when it rewrites the
	// npmrc, so the line is recognised by this flag plus its value.
	LoglevelAdded bool `json:"loglevel_added,omitempty"`
}

// loglevelLine makes every npm command show what it does (consona's
// output is verbose too; `consona ... --quiet` turns both down).
const loglevelLine = "loglevel=verbose"

// isConsonaLoglevel reports whether line is the loglevel line consona
// wrote (given that the state says it wrote one).
func isConsonaLoglevel(line string, added bool) bool {
	k, v, ok := kv(line)
	return added && ok && k == "loglevel" && v == "verbose"
}

// OwnsLoglevel reports whether `consona setup` added loglevel=verbose to
// the user npmrc (and it is still there).
func OwnsLoglevel() bool {
	if !loadState().LoglevelAdded {
		return false
	}
	b, _ := os.ReadFile(UserConfig())
	for _, l := range strings.Split(string(b), "\n") {
		if isConsonaLoglevel(l, true) {
			return true
		}
	}
	return false
}

// SetupNpmrc is UpdateNpmrc plus consona's loglevel line: added inside the
// block unless the user has a loglevel line of their own (a previous
// consona line, known from ownedLoglevel, is not the user's). added says
// whether the result carries consona's line.
func SetupNpmrc(content, baseURL string, owners []string, ownedLoglevel bool) (out string, added bool, err error) {
	var lines []string
	userLevel := false
	for _, l := range strings.Split(content, "\n") {
		if k, _, ok := kv(l); ok && k == "loglevel" {
			if isConsonaLoglevel(l, ownedLoglevel) {
				continue
			}
			userLevel = true
		}
		lines = append(lines, l)
	}
	out, err = UpdateNpmrc(strings.Join(lines, "\n"), baseURL, owners)
	if err != nil || userLevel {
		return out, false, err
	}
	return strings.TrimSuffix(out, blockEnd+"\n") + loglevelLine + "\n" + blockEnd + "\n", true, nil
}

func statePath() string { return filepath.Join(core.Home(), "npm-setup.json") }

func loadState() state {
	var s state
	b, _ := os.ReadFile(statePath())
	_ = json.Unmarshal(b, &s)
	return s
}

// Upstream is the registry public requests go to (the user's registry
// before setup), without trailing slash.
func Upstream() string {
	if u := loadState().Upstream; strings.HasPrefix(u, "http") {
		return strings.TrimRight(u, "/")
	}
	return strings.TrimRight(DefaultUpstream, "/")
}

func kv(line string) (string, string, bool) {
	l := strings.TrimSpace(line)
	if l == "" || l[0] == '#' || l[0] == ';' {
		return "", "", false
	}
	k, v, ok := strings.Cut(l, "=")
	return strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`), ok
}

// isDaemonURL matches any consona daemon registry URL.
var daemonURL = regexp.MustCompile(`^http://127\.0\.0\.1:\d+/npm/?$`)
var daemonNerf = regexp.MustCompile(`^//127\.0\.0\.1:\d+/npm/:_authToken$`)

// lineKind classifies a npmrc line written by consona setup.
func lineKind(line string) (kind, key, val string) {
	if line == blockStart || line == blockEnd || strings.HasPrefix(line, "# >>> consona") || strings.HasPrefix(line, "# consona-upstream=") {
		return "marker", "", ""
	}
	k, v, ok := kv(line)
	switch {
	case !ok:
	case k == "registry" && daemonURL.MatchString(v):
		return "registry", k, v
	case strings.HasSuffix(k, ":registry") && strings.HasPrefix(k, "@") && daemonURL.MatchString(v):
		return "scope", strings.TrimSuffix(strings.TrimLeft(k, "@"), ":registry"), v
	case daemonNerf.MatchString(k):
		return "token", k, v
	}
	for _, r := range retryLines {
		if strings.ReplaceAll(strings.TrimSpace(line), " ", "") == r {
			return "retry", "", ""
		}
	}
	return "", k, v
}

// RoutedScopes returns the owners routed to consona in the user npmrc.
func RoutedScopes() map[string]bool {
	b, _ := os.ReadFile(UserConfig())
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, s, _ := lineKind(l); k == "scope" {
			out[strings.ToLower(s)] = true
		}
	}
	return out
}

// nerfDart is npm's credential key prefix for a registry URL.
func nerfDart(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	path := p.Path
	if !strings.HasSuffix(path, "/") {
		path = path[:strings.LastIndex(path, "/")+1]
	}
	return "//" + p.Host + path
}

var envRef = regexp.MustCompile(`\$\{([^}?]+)\??\}`)

// Setup writes (or updates) the consona block at the end of the user npmrc
// and records the previous registry. Nothing secret is written.
func (Adapter) Setup(_ context.Context, baseURL string, owners []string) (string, error) {
	path := UserConfig()
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	st := loadState()
	for _, l := range strings.Split(string(old), "\n") {
		if kind, k, v := lineKind(l); kind == "" && k == "registry" && strings.HasPrefix(v, "http") {
			st.Upstream, st.RegistryLine = v, strings.TrimSpace(l) // last one wins, like npm
		}
	}
	if st.Upstream == "" { // e.g. set in the global npmrc or environment
		if v, ok := selfClient().lookup("registry"); ok && strings.HasPrefix(v.val, "http") && !daemonURL.MatchString(v.val) {
			st.Upstream = v.val
		}
	}
	if st.Upstream == "" {
		st.Upstream = DefaultUpstream
	}
	out, added, err := SetupNpmrc(string(old), baseURL, owners, st.LoglevelAdded)
	if err != nil {
		return "", err
	}
	st.LoglevelAdded = added
	if err := os.MkdirAll(filepath.Dir(statePath()), 0o700); err != nil {
		return "", err
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(statePath(), b, 0o600); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(out), 0o600)
}

// UpdateNpmrc returns content with all consona lines replaced by one block,
// at the end, pointing npm's registry and the union of existing and given
// owner scopes at baseURL.
func UpdateNpmrc(content, baseURL string, owners []string) (string, error) {
	set := map[string]bool{}
	tokenLine := strings.TrimPrefix(baseURL, "http:") + ":_authToken=" + Placeholder
	var keep []string
	for _, l := range strings.Split(content, "\n") {
		switch kind, k, v := lineKind(l); kind {
		case "scope":
			set[strings.ToLower(k)] = true
		case "token":
			if v != Placeholder { // a real login made through consona: keep it
				tokenLine = strings.TrimSpace(l)
			}
		case "":
			keep = append(keep, l)
		}
	}
	for _, o := range owners {
		o = strings.ToLower(strings.TrimLeft(strings.TrimSpace(o), "@"))
		if o == "" {
			continue
		}
		if strings.ContainsAny(o, "/ \t=") {
			return "", fmt.Errorf("invalid scope %q", o)
		}
		set[o] = true
	}
	scopes := make([]string, 0, len(set))
	for s := range set {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	// Any non-consona line for one of our scopes would bypass us; drop it.
	var body []string
	for _, l := range keep {
		drop := false
		for _, s := range scopes {
			if strings.HasPrefix(strings.TrimSpace(l), "@"+s+":registry=") {
				drop = true
			}
		}
		if !drop {
			body = append(body, l)
		}
	}
	text := strings.TrimRight(strings.Join(body, "\n"), "\n")
	if text != "" {
		text += "\n"
	}
	text += blockStart + "\nregistry=" + baseURL + "\n" + tokenLine + "\n" + strings.Join(retryLines, "\n") + "\n"
	for _, s := range scopes {
		text += "@" + s + ":registry=" + baseURL + "\n"
	}
	return text + blockEnd + "\n", nil
}

// Teardown removes every consona line (loglevel=verbose only if consona
// added it), restores the user's own registry
// line if npm dropped it, and moves a login made through consona to the
// upstream registry's key (unless one is already there).
func (Adapter) Teardown(context.Context) (string, error) {
	path := UserConfig()
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	st := loadState()
	upNerf := nerfDart(Upstream() + "/")
	var keep []string
	var login string
	hasRegistry, hasUpToken := false, false
	for _, l := range strings.Split(string(old), "\n") {
		kind, k, v := lineKind(l)
		switch {
		case kind == "token" && v != Placeholder:
			login = v
		case kind != "", isConsonaLoglevel(l, st.LoglevelAdded):
		default:
			keep = append(keep, l)
			hasRegistry = hasRegistry || k == "registry"
			hasUpToken = hasUpToken || k == upNerf+":_authToken"
		}
	}
	text := strings.TrimRight(strings.Join(keep, "\n"), "\n")
	if !hasRegistry && st.RegistryLine != "" {
		text += "\n" + st.RegistryLine
	}
	if login != "" && !hasUpToken {
		text += "\n" + upNerf + ":_authToken=" + login
	}
	text = strings.TrimLeft(text, "\n")
	if text != "" {
		text += "\n"
	}
	if text == "" { // the npmrc only had consona's lines: it did not exist before
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	} else if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	_ = os.Remove(statePath())
	return path, nil
}

// CheckSetup verifies npm's registry is the daemon.
func (Adapter) CheckSetup(baseURL string) error {
	b, _ := os.ReadFile(UserConfig())
	for _, l := range strings.Split(string(b), "\n") {
		if kind, _, v := lineKind(l); kind == "registry" && v == baseURL {
			return nil
		}
	}
	return fmt.Errorf("npm is not set up for consona (%s); run `consona setup` once", UserConfig())
}
