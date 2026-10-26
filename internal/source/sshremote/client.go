package sshremote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/identity"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
)

// ErrNoConsona means the remote is reachable but cannot run consona.
var ErrNoConsona = errors.New("consona is not installed on the remote")

// DefaultRemoteCmd runs consona on the remote from a non-interactive
// shell: common install dirs are added to PATH and, if npm is missing,
// nvm is loaded (remote builds need npm).
const DefaultRemoteCmd = `sh -c 'PATH="$HOME/.local/bin:$HOME/go/bin:$HOME/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; ` +
	`command -v npm >/dev/null 2>&1 || { [ -s "$HOME/.nvm/nvm.sh" ] && . "$HOME/.nvm/nvm.sh" >/dev/null 2>&1; }; ` +
	`command -v consona >/dev/null 2>&1 || { echo "consona-not-found" >&2; exit 127; }; exec consona remote-serve'`

// Client runs ssh.
type Client struct {
	ConnectTimeout time.Duration // ssh ConnectTimeout
	ControlDir     string        // ssh connection sharing sockets; "" = off
	SSHConfig      string        // passed as -F when set
	DefaultCmd     string        // remote command for remotes without RemoteCmd ("" = DefaultRemoteCmd)
}

// NewClient configures ssh from the environment: CONSONA_SSH_CONFIG,
// CONSONA_REMOTE_CMD, CONSONA_SSH_CONTROL=0 (no connection sharing).
func NewClient(home string) *Client {
	c := &Client{ConnectTimeout: 3 * time.Second, SSHConfig: os.Getenv("CONSONA_SSH_CONFIG"), DefaultCmd: os.Getenv("CONSONA_REMOTE_CMD")}
	if runtime.GOOS != "windows" && os.Getenv("CONSONA_SSH_CONTROL") != "0" {
		c.ControlDir = filepath.Join(home, "ssh")
	}
	return c
}

func (c *Client) remoteCommand(r Remote) string {
	switch {
	case r.RemoteCmd != "":
		return r.RemoteCmd + " remote-serve"
	case c.DefaultCmd != "":
		return c.DefaultCmd + " remote-serve"
	}
	return DefaultRemoteCmd
}

// Args returns the ssh arguments for r (exported for tests).
func (c *Client) Args(r Remote) []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", int(c.ConnectTimeout.Seconds())),
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
	if c.SSHConfig != "" {
		args = append(args, "-F", c.SSHConfig)
	}
	if r.Port != 0 {
		args = append(args, "-p", fmt.Sprint(r.Port))
	}
	if r.Identity != "" {
		args = append(args, "-i", r.Identity)
	}
	if c.ControlDir != "" {
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join(c.ControlDir, "%C"), "-o", "ControlPersist=60")
	}
	return append(args, "--", r.Target, c.remoteCommand(r))
}

type limitedBuf struct{ bytes.Buffer }

func (l *limitedBuf) Write(p []byte) (int, error) {
	if l.Len() < 8192 {
		l.Buffer.Write(p)
	}
	return len(p), nil
}

// Do sends one request and reads the response line; body, if set, reads
// what follows a successful response (a fetch payload).
func (c *Client) Do(ctx context.Context, r Remote, req Request, body func(*Response, *bufio.Reader) error) (*Response, error) {
	if err := r.Validate(); err != nil && !r.Auto {
		return nil, err
	}
	if strings.HasPrefix(r.Target, "-") {
		return nil, fmt.Errorf("invalid ssh target %q", r.Target)
	}
	if c.ControlDir != "" {
		_ = identity.EnsureDir(c.ControlDir)
	}
	cmd := exec.CommandContext(ctx, "ssh", c.Args(r)...)
	cmd.WaitDelay = 2 * time.Second // a ControlPersist master may keep a pipe open
	line, _ := json.Marshal(req)
	cmd.Stdin = bytes.NewReader(append(line, '\n'))
	var stderr limitedBuf
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot run ssh: %w", err)
	}
	rd := bufio.NewReaderSize(stdout, 64<<10)
	var resp Response
	var perr error
	first, rerr := rd.ReadBytes('\n')
	switch {
	case len(bytes.TrimSpace(first)) == 0:
		perr = errors.New("no answer")
	case json.Unmarshal(first, &resp) != nil:
		perr = fmt.Errorf("unexpected output from the remote (is the remote command printing to stdout?): %.120q", first)
	case resp.Protocol != Protocol:
		perr = fmt.Errorf("remote speaks %q, this consona speaks %q: update consona on both machines", resp.Protocol, Protocol)
	case resp.OK && body != nil:
		perr = body(&resp, rd)
	}
	_ = rerr
	if perr != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, rd)
	werr := cmd.Wait()
	if perr != nil && len(bytes.TrimSpace(first)) == 0 {
		return nil, c.explain(r, werr, stderr.String(), ctx.Err())
	}
	if perr != nil {
		return nil, fmt.Errorf("remote %s: %w", r.Label(), perr)
	}
	return &resp, nil
}

// explain turns an ssh failure into one clear sentence.
func (c *Client) explain(r Remote, werr error, stderr string, cerr error) error {
	last := strings.TrimSpace(stderr)
	if i := strings.LastIndex(last, "\n"); i >= 0 {
		last = strings.TrimSpace(last[i+1:])
	}
	code := -1
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		code = ee.ExitCode()
	}
	switch {
	case cerr != nil:
		return fmt.Errorf("remote %s: timed out (%v)", r.Label(), cerr)
	case strings.Contains(stderr, "consona-not-found") || code == 127 || strings.Contains(stderr, "command not found"):
		return fmt.Errorf("remote %s: %w (or not on its non-interactive PATH): install consona there, or set the command with `consona remote add %s %s --remote-cmd /path/to/consona --replace`", r.Label(), ErrNoConsona, r.Name, r.Target)
	case strings.Contains(stderr, "Host key verification failed"):
		return fmt.Errorf("remote %s: host key not known or changed; connect once with `ssh %s` to check and accept it (consona never skips host key checks)", r.Label(), r.Target)
	case strings.Contains(stderr, "Permission denied"):
		return fmt.Errorf("remote %s: ssh key not accepted (%s); consona uses BatchMode, so it never asks for passwords: load your key in ssh-agent or pass --identity", r.Label(), last)
	case code == 255:
		return fmt.Errorf("remote %s: ssh failed: %s", r.Label(), last)
	}
	if last == "" && werr != nil {
		last = werr.Error()
	}
	return fmt.Errorf("remote %s: no answer from consona remote-serve: %s", r.Label(), last)
}

// remoteError converts an error response.
func remoteError(r Remote, resp *Response) error {
	if resp.NotFound {
		return fmt.Errorf("remote %s: %s: %w", r.Label(), resp.Error, core.ErrNotFound)
	}
	return fmt.Errorf("remote %s: %s", r.Label(), resp.Error)
}

// State is what consona last learned about a remote.
type State struct {
	Name      string           `json:"name"`
	Target    string           `json:"target"`
	Auto      bool             `json:"auto"`
	At        time.Time        `json:"at"`
	OK        bool             `json:"ok"`
	Err       string           `json:"err,omitempty"`
	NoConsona bool             `json:"no_consona,omitempty"`
	Names     []string         `json:"names,omitempty"`
	Username  string           `json:"username,omitempty"`
	Device    *identity.Device `json:"device,omitempty"`
	Latency   time.Duration    `json:"latency"`
	Consona   string           `json:"consona,omitempty"`

	refreshing bool
}

func (s *State) has(name string) bool {
	for _, n := range s.Names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// Manager resolves names across remotes: explicit ones first (by name),
// then hosts auto-detected from the ssh config. Package lists are cached
// (in memory and in StatePath): fresh for ListTTL, used while refreshed in
// the background up to StaleTTL; unreachable remotes are skipped for
// DownTTL, remotes without consona for NoConsonaTTL. So a name that is not
// on any remote costs no ssh round trip once the lists are known.
type Manager struct {
	Client     *Client
	Store      *Store
	StatePath  string
	AutoDetect bool

	ListTTL, StaleTTL, DownTTL, NoConsonaTTL time.Duration
	ProbeTimeout                             time.Duration // one list probe
	OpTimeout                                time.Duration // resolve/fetch (may build on the remote)

	mu     sync.Mutex
	states map[string]*State // by Remote.Key
	pkgs   map[string]*Package
	loaded bool
	now    func() time.Time
}

// NewManager returns a manager using consona home for state and sockets.
func NewManager(home string) *Manager {
	return &Manager{
		Client: NewClient(home), Store: OpenStore(), StatePath: filepath.Join(home, "remotes-state.json"),
		AutoDetect: os.Getenv("CONSONA_SSH_AUTODETECT") != "0",
		ListTTL:    60 * time.Second, StaleTTL: 30 * time.Minute, DownTTL: 5 * time.Minute, NoConsonaTTL: 30 * time.Minute,
		ProbeTimeout: 8 * time.Second, OpTimeout: core.BuildTimeout,
		states: map[string]*State{}, pkgs: map[string]*Package{}, now: time.Now,
	}
}

// Remotes lists explicit remotes, then ssh-config hosts not already
// covered by an explicit remote (same name or target).
func (m *Manager) Remotes() []Remote {
	rs, _ := m.Store.List()
	if !m.AutoDetect {
		return rs
	}
	taken := map[string]bool{}
	for _, r := range rs {
		taken[r.Name] = true
		taken[r.Target] = true
	}
	for _, h := range ParseSSHConfig(SSHConfigPath()) {
		if taken[h.Alias] {
			continue
		}
		rs = append(rs, Remote{Name: h.Alias, Target: h.Alias, Auto: true})
	}
	return rs
}

func (m *Manager) load() {
	if m.loaded {
		return
	}
	m.loaded = true
	b, err := os.ReadFile(m.StatePath)
	if err != nil {
		return
	}
	var st map[string]*State
	if json.Unmarshal(b, &st) == nil {
		for k, v := range st {
			m.states[k] = v
		}
	}
}

func (m *Manager) save() {
	b, _ := json.MarshalIndent(m.states, "", "  ")
	_ = identity.WriteFileAtomic(m.StatePath, b, 0o600)
}

// States returns a copy of the cached state per remote key.
func (m *Manager) States() map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	out := map[string]State{}
	for k, v := range m.states {
		out[k] = *v
	}
	return out
}

// Probe lists r's packages now and caches the result.
func (m *Manager) Probe(ctx context.Context, r Remote) State {
	ctx, cancel := context.WithTimeout(ctx, m.ProbeTimeout)
	defer cancel()
	t := time.Now()
	resp, err := m.Client.Do(ctx, r, Request{Op: "list"}, nil)
	st := &State{Name: r.Name, Target: r.Target, Auto: r.Auto, At: m.now(), Latency: time.Since(t)}
	switch {
	case err != nil:
		st.Err, st.NoConsona = err.Error(), errors.Is(err, ErrNoConsona)
	case !resp.OK:
		st.Err = remoteError(r, resp).Error()
	default:
		st.OK, st.Username, st.Device, st.Consona = true, resp.Username, resp.Device, resp.Consona
		for _, p := range resp.Packages {
			st.Names = append(st.Names, p.Name)
		}
		sort.Strings(st.Names)
	}
	m.mu.Lock()
	m.load()
	m.states[r.Key()] = st
	m.save()
	m.mu.Unlock()
	return *st
}

// freshness: 0 = usable, 1 = usable but refresh in background, 2 = probe now.
func (m *Manager) freshness(st *State) int {
	if st == nil {
		return 2
	}
	age := m.now().Sub(st.At)
	switch {
	case st.OK && age < m.ListTTL:
		return 0
	case st.OK && age < m.StaleTTL:
		return 1
	case !st.OK && st.NoConsona && age < m.NoConsonaTTL:
		return 0
	case !st.OK && !st.NoConsona && age < m.DownTTL:
		return 0
	}
	return 2
}

// Find returns the first remote (in order) that has name registered.
func (m *Manager) Find(ctx context.Context, name string) (*Remote, *State) {
	rs := m.Remotes()
	if len(rs) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	m.load()
	var need []Remote
	for _, r := range rs {
		st := m.states[r.Key()]
		switch m.freshness(st) {
		case 1:
			if !st.refreshing {
				st.refreshing = true
				go m.Probe(context.Background(), r)
			}
		case 2:
			need = append(need, r)
		}
	}
	m.mu.Unlock()
	if len(need) > 0 {
		var wg sync.WaitGroup
		for _, r := range need {
			wg.Add(1)
			go func(r Remote) { defer wg.Done(); m.Probe(ctx, r) }(r)
		}
		wg.Wait()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		if st := m.states[r.Key()]; st != nil && st.OK && st.has(name) {
			r := r
			c := *st
			return &r, &c
		}
	}
	return nil, nil
}

// Scopes are the scopes of packages known on reachable remotes.
func (m *Manager) Scopes() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	out := map[string]bool{}
	for _, st := range m.states {
		if !st.OK {
			continue
		}
		for _, n := range st.Names {
			if s, _, err := localindex.SplitName(strings.ToLower(n)); err == nil {
				out[s] = true
			}
		}
	}
	return out
}

// CommitPrefix marks revisions served by a remote: ssh-<key>-<hash>.
const CommitPrefix = "ssh-"

var hexRe = regexp.MustCompile(`^[0-9a-f]{32,128}$`)

// Revisions resolves ref on remote r (the remote packs it if needed).
func (m *Manager) Revisions(ctx context.Context, r Remote, ref core.Ref) ([]core.Revision, error) {
	name := "@" + ref.Owner + "/" + ref.Name
	ctx, cancel := context.WithTimeout(ctx, m.OpTimeout)
	defer cancel()
	resp, err := m.Client.Do(ctx, r, Request{Op: "resolve", Name: name}, nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK || resp.Package == nil {
		return nil, remoteError(r, resp)
	}
	p := resp.Package
	switch {
	case !core.ValidVersion(p.Version):
		return nil, fmt.Errorf("remote %s sent an invalid version %q", r.Label(), p.Version)
	case !strings.EqualFold(p.Name, name):
		return nil, fmt.Errorf("remote %s answered for %q instead of %q", r.Label(), p.Name, name)
	case !hexRe.MatchString(p.Hash):
		return nil, fmt.Errorf("remote %s sent an invalid content hash", r.Label())
	case p.SHA512 == "" || p.Size <= 0:
		return nil, fmt.Errorf("remote %s sent no integrity for %s", r.Label(), name)
	}
	p.File = npmpack.FileName(name, p.Version) // never trust a remote file name
	commit := CommitPrefix + r.Key() + "-" + p.Hash[:32]
	m.mu.Lock()
	m.pkgs[commit] = p
	m.mu.Unlock()
	return []core.Revision{{Version: p.Version, Commit: commit, Head: true}}, nil
}

func (m *Manager) pkg(rev core.Revision) (*Package, *Remote, error) {
	m.mu.Lock()
	p := m.pkgs[rev.Commit]
	m.mu.Unlock()
	if p == nil {
		return nil, nil, fmt.Errorf("%s: remote package metadata expired; run the install again", rev.Version)
	}
	key := strings.TrimPrefix(rev.Commit, CommitPrefix)
	if i := strings.Index(key, "-"); i > 0 {
		key = key[:i]
	}
	for _, r := range m.Remotes() {
		if r.Key() == key {
			r := r
			return p, &r, nil
		}
	}
	return nil, nil, fmt.Errorf("the remote that served %s@%s is no longer configured", p.Name, p.Version)
}

// PrebuiltInfo implements core.Prebuilt for remote revisions.
func (m *Manager) PrebuiltInfo(_ context.Context, _ core.Ref, rev core.Revision) (*core.Info, bool, error) {
	if !strings.HasPrefix(rev.Commit, CommitPrefix) {
		return nil, false, nil
	}
	p, _, err := m.pkg(rev)
	if err != nil {
		return nil, true, err
	}
	return &core.Info{Version: p.Version, Manifest: p.Manifest}, true, nil
}

// FetchPrebuilt streams the tarball over ssh into outDir, verifying size,
// sha512 (announced at resolve time and again by fetch) and sha1.
func (m *Manager) FetchPrebuilt(ctx context.Context, _ core.Ref, rev core.Revision, outDir string) (*core.Artifact, bool, error) {
	if !strings.HasPrefix(rev.Commit, CommitPrefix) {
		return nil, false, nil
	}
	p, r, err := m.pkg(rev)
	if err != nil {
		return nil, true, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.OpTimeout)
	defer cancel()
	tmp, err := os.CreateTemp(outDir, ".fetch-")
	if err != nil {
		return nil, true, err
	}
	defer os.Remove(tmp.Name())
	h512, h1 := sha512.New(), sha1.New()
	_, err = m.Client.Do(ctx, *r, Request{Op: "fetch", Name: p.Name, Version: p.Version}, func(resp *Response, rd *bufio.Reader) error {
		if resp.Size != p.Size || resp.SHA512 != p.SHA512 {
			return fmt.Errorf("%s@%s changed on the remote between resolve and fetch; run the install again", p.Name, p.Version)
		}
		n, err := io.CopyN(io.MultiWriter(tmp, h512, h1), rd, resp.Size)
		if err != nil {
			return fmt.Errorf("tarball truncated after %d of %d bytes: %w", n, resp.Size, err)
		}
		return nil
	})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, true, err
	}
	if got := base64.StdEncoding.EncodeToString(h512.Sum(nil)); got != p.SHA512 {
		return nil, true, fmt.Errorf("%s@%s from remote %s failed the integrity check (sha512 mismatch); not installed", p.Name, p.Version, r.Label())
	}
	if p.SHA1 != "" && hex.EncodeToString(h1.Sum(nil)) != p.SHA1 {
		return nil, true, fmt.Errorf("%s@%s from remote %s failed the integrity check (sha1 mismatch); not installed", p.Name, p.Version, r.Label())
	}
	if err := os.Rename(tmp.Name(), filepath.Join(outDir, p.File)); err != nil {
		return nil, true, err
	}
	return &core.Artifact{Version: p.Version, File: p.File, Manifest: p.Manifest, SHA1: p.SHA1, SHA512: p.SHA512}, true, nil
}

// Warm refreshes every remote whose package list is not fresh, in
// parallel; the daemon calls it at start so scopes are known early.
func (m *Manager) Warm(ctx context.Context) {
	m.mu.Lock()
	m.load()
	var need []Remote
	for _, r := range m.Remotes() {
		if m.freshness(m.states[r.Key()]) != 0 {
			need = append(need, r)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range need {
		wg.Add(1)
		go func(r Remote) { defer wg.Done(); m.Probe(ctx, r) }(r)
	}
	wg.Wait()
}
