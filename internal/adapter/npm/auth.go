package npm

import (
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The daemon sends the user's existing login for the upstream registry,
// found the way npm finds it: npm_config_* environment variables, then the
// project .npmrc, the user npmrc, the global npmrc, with ${VAR} references
// expanded. On Linux the daemon looks at the npm process on the other end
// of the connection (same user only) for its environment and working
// directory; elsewhere it uses its own environment and no project npmrc.

// client is what npm's config depends on: its environment, working
// directory and node binary.
type client struct {
	env  map[string]string
	cwd  string // "" = unknown: no project npmrc
	node string // node executable, for the global npmrc
}

func (c *client) getenv(k string) string {
	if v, ok := c.env[k]; ok {
		return v
	}
	for ek, v := range c.env { // npm matches npm_config_ case-insensitively
		if strings.EqualFold(ek, k) {
			return v
		}
	}
	return ""
}

func selfClient() *client {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	node := ""
	if p, err := execLookPath("node"); err == nil {
		node = p
	}
	return &client{env: env, node: node}
}

// localPrefix is npm's project root: the nearest directory up from cwd with
// a package.json or node_modules, else cwd.
func localPrefix(cwd string) string {
	for d := cwd; ; {
		for _, f := range []string{"package.json", "node_modules"} {
			if _, err := os.Stat(filepath.Join(d, f)); err == nil {
				return d
			}
		}
		p := filepath.Dir(d)
		if p == d {
			return cwd
		}
		d = p
	}
}

func (c *client) userFile() string {
	if u := c.getenv("npm_config_userconfig"); u != "" {
		return u
	}
	home := c.getenv("HOME")
	if home == "" {
		home = c.getenv("USERPROFILE")
	}
	return filepath.Join(home, ".npmrc")
}

// configFiles lists npmrc files in npm's precedence order (highest first).
func (c *client) configFiles() []string {
	user := c.userFile()
	var files []string
	if c.cwd != "" {
		if p := filepath.Join(localPrefix(c.cwd), ".npmrc"); p != user {
			files = append(files, p)
		}
	}
	files = append(files, user)
	global := c.getenv("npm_config_globalconfig")
	if global == "" {
		prefix := c.getenv("npm_config_prefix")
		if prefix == "" {
			prefix = c.getenv("PREFIX")
		}
		if prefix == "" && c.node != "" {
			prefix = filepath.Dir(c.node)
			if runtime.GOOS != "windows" {
				prefix = filepath.Dir(prefix)
			}
		}
		if prefix != "" {
			global = filepath.Join(prefix, "etc", "npmrc")
		}
	}
	if global != "" {
		files = append(files, global)
	}
	return files
}

// setting is one config value and where it came from.
type setting struct {
	val, file, line string // file "" = environment
	literal         bool   // written as-is (no ${VAR})
}

// lookup returns the value npm would use for key.
func (c *client) lookup(key string) (setting, bool) {
	if v := c.getenv("npm_config_" + key); v != "" {
		return setting{val: v}, true
	}
	for _, f := range c.configFiles() {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var found *setting
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := kv(l); ok && k == key {
				s := setting{val: c.expand(v), file: f, line: strings.TrimSpace(l), literal: !envRef.MatchString(v)}
				found = &s // last one wins, as in npm's ini parser
			}
		}
		if found != nil {
			return *found, true
		}
	}
	return setting{}, false
}

func (c *client) expand(v string) string {
	return envRef.ReplaceAllStringFunc(v, func(m string) string {
		return c.getenv(envRef.FindStringSubmatch(m)[1])
	})
}

// login is the user's credential for a registry, as npm would send it.
type login struct {
	header string  // Authorization value, "" if none
	token  string  // bearer token, if that is the kind
	src    setting // where the token came from
}

func (c *client) loginFor(registry string) login {
	nerf := nerfDart(strings.TrimRight(registry, "/") + "/")
	if s, ok := c.lookup(nerf + ":_authToken"); ok && s.val != "" {
		return login{header: "Bearer " + s.val, token: s.val, src: s}
	}
	if s, ok := c.lookup(nerf + ":_auth"); ok && s.val != "" {
		return login{header: "Basic " + s.val}
	}
	u, okU := c.lookup(nerf + ":username")
	p, okP := c.lookup(nerf + ":_password")
	if okU && okP && u.val != "" {
		pw, _ := base64.StdEncoding.DecodeString(p.val)
		return login{header: "Basic " + base64.StdEncoding.EncodeToString([]byte(u.val+":"+string(pw)))}
	}
	return login{}
}

// clientFor identifies the npm process behind r. ok is false if it belongs
// to another user (their requests never get this user's login).
func clientFor(r *http.Request) (*client, bool) {
	c, sameUser, found := peerClient(r)
	if !found {
		return selfClient(), true
	}
	return c, sameUser
}

// forgetLogin removes a revoked token line from the user's own npmrc, so a
// logout through consona leaves no stale login behind. npm rewrites the
// whole file after the logout request (dropping the placeholder), which
// would bring the line back, so this waits for that write first.
func forgetLogin(s setting) {
	if s.file == "" || !s.literal {
		return
	}
	for i := 0; i < 100; i++ {
		b, _ := os.ReadFile(s.file)
		if !strings.Contains(string(b), Placeholder) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != s.line {
			keep = append(keep, l)
		}
	}
	_ = os.WriteFile(s.file, []byte(strings.Join(keep, "\n")), 0o600)
}

var execLookPath = exec.LookPath
