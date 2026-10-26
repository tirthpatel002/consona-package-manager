// Package localcli implements consona's device-local commands: identity
// (init, whoami), local package registration (register, registered,
// unregister), SSH remotes (remote add|list|remove|test, remote-serve) and
// `which`. It also builds the daemon's source chain. main.go only calls
// Run, NewDaemonSource, ExtraOwners and AfterInstall.
package localcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/identity"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/source/chain"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
	"github.com/tirthpatel002/consona-package-manager/internal/source/sshremote"
)

// Version is consona's version (set by main).
var Version = "dev"

// Usage lists the commands handled here.
const Usage = `
  consona init [--username NAME] [--force]
                                       one-time per device: your username (scope @NAME) and a device id
  consona whoami                       print username and this device
  consona register <folder> [--name @scope/pkg] [--replace] [--allow-scope]
                                       serve a package folder from this device (live: every install gets its
                                       current content); local folders win over remotes and GitHub
  consona registered                   list folders registered on this device
  consona unregister <@scope/pkg>      stop serving a folder (the folder is not touched)
  consona remote add <name> <user@host|ssh-alias> [--port N] [--identity FILE] [--remote-cmd PATH] [--replace]
  consona remote list|remove <name>|test [name]
                                       other devices you can ssh into; their registered packages install here
                                       (hosts in ~/.ssh/config are also tried, after explicit remotes)
  consona which <@scope/pkg>           where a package would come from
`

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "consona: "+format+"\n", a...)
	return 1
}

// Run handles a local command; ok is false if args[0] is not one.
func Run(args []string) (code int, ok bool) {
	if len(args) == 0 {
		return 0, false
	}
	ctx := context.Background()
	switch args[0] {
	case "init":
		return cmdInit(args[1:]), true
	case "whoami":
		return cmdWhoami(), true
	case "register":
		return cmdRegister(ctx, args[1:]), true
	case "registered":
		return cmdRegistered(), true
	case "unregister":
		return cmdUnregister(args[1:]), true
	case "remote":
		return cmdRemote(ctx, args[1:]), true
	case "remote-serve":
		return cmdServe(ctx), true
	case "which":
		return cmdWhich(ctx, args[1:]), true
	}
	return 0, false
}

// flags splits args into positionals and --flag[=value] options; names in
// valued take the next argument as value.
func flags(args []string, valued ...string) (pos []string, opt map[string]string, err error) {
	opt = map[string]string{}
	isValued := map[string]bool{}
	for _, v := range valued {
		isValued[v] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		k, v, has := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if isValued[k] && !has {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--%s needs a value", k)
			}
			i++
			v = args[i]
		} else if !isValued[k] && has {
			return nil, nil, fmt.Errorf("--%s takes no value", k)
		}
		opt[k] = v
	}
	return pos, opt, nil
}

func checkOpts(opt map[string]string, allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range opt {
		if !ok[k] {
			return fmt.Errorf("unknown option --%s", k)
		}
	}
	return nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func cmdInit(args []string) int {
	pos, opt, err := flags(args, "username")
	if err == nil {
		err = checkOpts(opt, "username", "force")
	}
	if err != nil || len(pos) > 0 {
		return fail("usage: consona init [--username NAME] [--force]")
	}
	name := opt["username"]
	if name == "" {
		if id, err := identity.Load(); err == nil {
			fmt.Printf("consona: already initialised as @%s on device %s (change with `consona init --username NAME --force`)\n", id.Username, id.Device.Short())
			return 0
		}
		if !isTerminal(os.Stdin) {
			return fail("consona init needs --username NAME (no terminal to ask on)")
		}
		fmt.Print("consona username (your private scope, e.g. tirth -> @tirth/pkg): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if name = strings.TrimSpace(line); name == "" {
			return fail("consona init needs a username (--username NAME)")
		}
	}
	id, created, err := identity.Init(name, opt["force"] != "" || hasKey(opt, "force"))
	if err != nil {
		return fail("%v", err)
	}
	verb := "already initialised"
	if created {
		verb = "initialised"
	}
	fmt.Printf("consona: %s: username @%s, device %s (%s, %s/%s)\nconsona: config %s\n", verb, id.Username, id.Device.ID, id.Device.Hostname, id.Device.OS, id.Device.Arch, identity.Path())
	routeScopes(id.Username)
	return 0
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func cmdWhoami() int {
	id, err := identity.Load()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("username: @%s\ndevice:   %s\nhost:     %s (%s/%s)\ncreated:  %s\nconfig:   %s\n", id.Username, id.Device.ID, id.Device.Hostname, id.Device.OS, id.Device.Arch,
		id.Device.Created.Local().Format("2006-01-02 15:04 MST"), identity.ConfigDir())
	return 0
}

// routeScopes adds scopes to consona's npmrc block if npm is set up for
// consona (it never sets npm up by itself).
func routeScopes(scopes ...string) {
	a, ok := core.Lookup("npm")
	if !ok {
		return
	}
	base := daemon.Load().BaseURL() + "/npm/"
	if a.CheckSetup(base) != nil {
		return
	}
	have := npm.RoutedScopes()
	var add []string
	for _, s := range scopes {
		s = strings.ToLower(s)
		if s != "" && !have[s] {
			have[s] = true
			add = append(add, s)
		}
	}
	if len(add) == 0 {
		return
	}
	if path, err := a.Setup(context.Background(), base, add); err == nil {
		fmt.Printf("consona: npm routes @%s to consona (%s)\n", strings.Join(add, ", @"), path)
	} else {
		fmt.Fprintf(os.Stderr, "consona: warning: could not route @%s in the npmrc: %v\n", strings.Join(add, ", @"), err)
	}
}

func cmdRegister(ctx context.Context, args []string) int {
	pos, opt, err := flags(args, "name")
	if err == nil {
		err = checkOpts(opt, "name", "replace", "allow-scope")
	}
	if err != nil || len(pos) != 1 {
		return fail("usage: consona register <folder> [--name @scope/pkg] [--replace] [--allow-scope]")
	}
	id, err := identity.Load()
	if err != nil {
		return fail("%v", err)
	}
	dir, err := localindex.Canonical(pos[0])
	if err != nil {
		return fail("%s: %v", pos[0], err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fail("%s is not a folder", dir)
	}
	pj, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return fail("%s has no package.json: only npm package folders can be registered", dir)
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Private bool   `json:"private"`
	}
	if err := json.Unmarshal(pj, &m); err != nil {
		return fail("%s/package.json is not valid JSON: %v", dir, err)
	}
	if m.Version != "" && !core.ValidVersion(strings.TrimPrefix(m.Version, "v")) {
		return fail("%s/package.json: version %q is not semver", dir, m.Version)
	}
	var name string
	if n := opt["name"]; n != "" {
		if !strings.HasPrefix(n, "@") {
			n, err = localindex.DefaultName(n, id.Username)
			if err != nil {
				return fail("%v", err)
			}
		}
		scope, _, err := localindex.SplitName(strings.ToLower(n))
		if err != nil {
			return fail("%v", err)
		}
		if scope != id.Username && !hasKey(opt, "allow-scope") && !npm.RoutedScopes()[scope] && !localindex.Open().Scopes()[scope] {
			return fail("%s is outside your scope @%s and @%s is not a private scope yet; registering it would hide public @%s packages on this device. Pass --allow-scope if that is intended", n, id.Username, scope, scope)
		}
		name = strings.ToLower(n)
	} else {
		if m.Name == "" {
			return fail("%s/package.json has no name; pass --name @%s/<pkg>", dir, id.Username)
		}
		if name, err = localindex.DefaultName(m.Name, id.Username); err != nil {
			return fail("%v", err)
		}
	}
	e := localindex.Entry{Name: name, Path: dir, DeviceID: id.Device.ID, Registered: time.Now().UTC()}
	removed, err := localindex.Open().Add(e, hasKey(opt, "replace"))
	if err != nil {
		return fail("%v", err)
	}
	for _, r := range removed {
		fmt.Printf("consona: replaced %s -> %s\n", r.Name, r.Path)
	}
	src := local.New(localindex.Open(), core.Home())
	scope, pkg, _ := localindex.SplitName(name)
	cur, err := src.Resolve(core.Ref{Owner: scope, Name: pkg})
	if err != nil {
		return fail("registered, but: %v", err)
	}
	fmt.Printf("consona: registered %s -> %s (device %s)\nconsona: current version %s (changes to the folder publish a new version on the next install)\n", name, dir, id.Device.Short(), cur.Version)
	if m.Name != "" && !strings.EqualFold(m.Name, name) {
		fmt.Printf("consona: note: package.json says %q; it is served as %s (the packed package.json is renamed, your folder is not touched)\n", m.Name, name)
	}
	routeScopes(scope)
	githubShadow(ctx, core.Ref{Owner: scope, Name: pkg})
	return 0
}

// githubShadow notes (best effort, 3s) when a GitHub repo has the same name.
func githubShadow(ctx context.Context, ref core.Ref) {
	gh := github.New()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := gh.Revisions(ctx, ref); err == nil {
		fmt.Printf("consona: note: %s also exists on GitHub (%s); the local folder wins on this device\n", "@"+ref.String(), gh.Describe(ref))
	}
}

func cmdRegistered() int {
	es, err := localindex.Open().List()
	if err != nil {
		return fail("%v", err)
	}
	if len(es) == 0 {
		fmt.Println("consona: nothing registered on this device (consona register <folder>)")
		return 0
	}
	this := ""
	if id, err := identity.Load(); err == nil {
		this = id.Device.ID
	}
	src := local.New(localindex.Open(), core.Home())
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tFOLDER\tDEVICE")
	for _, e := range es {
		scope, pkg, _ := localindex.SplitName(e.Name)
		v := "?"
		if c, err := src.Resolve(core.Ref{Owner: scope, Name: pkg}); err == nil {
			v = c.Version
		} else {
			v = "error: " + err.Error()
		}
		dev := e.DeviceID
		if dev == this {
			dev = "this device"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, v, e.Path, dev)
	}
	tw.Flush()
	return 0
}

func cmdUnregister(args []string) int {
	if len(args) != 1 {
		return fail("usage: consona unregister <@scope/pkg>")
	}
	e, err := localindex.Open().Remove(args[0])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("consona: unregistered %s (folder %s left as is)\n", e.Name, e.Path)
	return 0
}

func cmdRemote(ctx context.Context, args []string) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	store := sshremote.OpenStore()
	switch sub {
	case "add":
		pos, opt, err := flags(args, "port", "identity", "remote-cmd")
		if err == nil {
			err = checkOpts(opt, "port", "identity", "remote-cmd", "replace", "no-test")
		}
		if err != nil || len(pos) != 2 {
			return fail("usage: consona remote add <name> <user@host|ssh-alias> [--port N] [--identity FILE] [--remote-cmd PATH] [--replace] [--no-test]")
		}
		r := sshremote.Remote{Name: pos[0], Target: pos[1], Identity: opt["identity"], RemoteCmd: opt["remote-cmd"], Added: time.Now().UTC()}
		if p := opt["port"]; p != "" {
			if r.Port, err = strconv.Atoi(p); err != nil || r.Port <= 0 || r.Port > 65535 {
				return fail("invalid --port %q", p)
			}
		}
		if r.Identity != "" {
			if r.Identity, err = filepath.Abs(expandHome(r.Identity)); err != nil {
				return fail("%v", err)
			}
		}
		if err := store.Add(r, hasKey(opt, "replace")); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("consona: added remote %s (%s)\n", r.Name, r.Label())
		if hasKey(opt, "no-test") {
			return 0
		}
		return testRemotes(ctx, []sshremote.Remote{r})
	case "list", "ls":
		m := sshremote.NewManager(core.Home())
		states := m.States()
		rs := m.Remotes()
		if len(rs) == 0 {
			fmt.Println("consona: no remotes (consona remote add <name> <user@host>; Host entries in ~/.ssh/config are used too)")
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tTARGET\tKIND\tLAST CHECK\tSTATUS")
		for _, r := range rs {
			kind := "explicit"
			if r.Auto {
				kind = "ssh-config"
			}
			when, status := "never", "-"
			if st, ok := states[r.Key()]; ok {
				when = st.At.Local().Format("Jan 02 15:04 MST")
				status = describeState(st)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Label(), kind, when, status)
		}
		tw.Flush()
		return 0
	case "remove", "rm":
		if len(args) != 1 {
			return fail("usage: consona remote remove <name>")
		}
		if err := store.Remove(args[0]); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("consona: removed remote %s\n", args[0])
		return 0
	case "test":
		m := sshremote.NewManager(core.Home())
		var rs []sshremote.Remote
		for _, r := range m.Remotes() {
			if len(args) == 0 || r.Name == args[0] {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			if len(args) > 0 {
				return fail("no remote named %q (consona remote list)", args[0])
			}
			return fail("no remotes")
		}
		return testRemotes(ctx, rs)
	}
	return fail("usage: consona remote add|list|remove|test")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	return p
}

func describeState(st sshremote.State) string {
	if !st.OK {
		return "error: " + st.Err
	}
	who := ""
	if st.Username != "" {
		who = "@" + st.Username + " "
	}
	dev := ""
	if st.Device != nil {
		dev = "device " + st.Device.Short() + ", "
	}
	return fmt.Sprintf("ok: %s%s%d package(s): %s", who, dev, len(st.Names), strings.Join(st.Names, " "))
}

func testRemotes(ctx context.Context, rs []sshremote.Remote) int {
	m := sshremote.NewManager(core.Home())
	code := 0
	var scopes []string
	for _, r := range rs {
		t := time.Now()
		st := m.Probe(ctx, r)
		fmt.Printf("consona: remote %s (%s): %s [%s]\n", r.Name, r.Label(), describeState(st), time.Since(t).Round(time.Millisecond))
		if !st.OK {
			code = 1
			continue
		}
		for _, n := range st.Names {
			if s, _, err := localindex.SplitName(strings.ToLower(n)); err == nil {
				scopes = append(scopes, s)
			}
		}
	}
	routeScopes(scopes...)
	return code
}

func cmdServe(ctx context.Context) int {
	// Builds on this machine may need its own consona daemon (npm here may
	// point at it); start it if npm is set up for consona.
	if a, ok := core.Lookup("npm"); ok {
		cfg := daemon.Load()
		if a.CheckSetup(cfg.BaseURL()+"/npm/") == nil {
			_ = daemon.Ensure(cfg)
		}
	}
	idx := localindex.Open()
	src := local.New(idx, core.Home())
	e := core.NewEngine(src, filepath.Join(core.Home(), "cache"))
	s := &sshremote.Server{Version: Version, Index: idx, Local: src, Engine: e, Builder: npmpack.Builder{}}
	if err := s.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "consona remote-serve:", err)
		return 1
	}
	return 0
}

// NewChain builds the resolution chain: local folders, ssh remotes, GitHub.
func NewChain(home string) *chain.Chain {
	idx := localindex.Open()
	c := &chain.Chain{Local: local.New(idx, home), Remotes: sshremote.NewManager(home)}
	if id, err := identity.Load(); err == nil {
		c.ThisDevice = id.Device.Short()
	}
	c.GitHub = github.New() // never fails: credentials are resolved per request
	return c
}

// NewDaemonSource is the daemon's source. Registered and remote scopes are
// served as private even when not routed in the npmrc. Remotes' package
// lists are refreshed in the background at start, so the first install
// does not wait for ssh.
func NewDaemonSource(home string) core.Source {
	c := NewChain(home)
	npm.ExtraScopes = c.PrivateScopes
	if c.GitHubError != nil {
		fmt.Fprintf(os.Stderr, "consona: GitHub source off: %v\n", c.GitHubError)
	}
	go c.Remotes.Warm(context.Background())
	return c
}

// ExtraOwners are scopes `consona setup` routes besides GitHub owners.
func ExtraOwners() []string {
	set := map[string]bool{}
	if id, err := identity.Load(); err == nil {
		set[id.Username] = true
	}
	for s := range localindex.Open().Scopes() {
		set[s] = true
	}
	for s := range sshremote.NewManager(core.Home()).Scopes() {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func cmdWhich(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return fail("usage: consona which <@scope/pkg>")
	}
	scope, pkg, err := localindex.SplitName(strings.ToLower(args[0]))
	if err != nil {
		return fail("%v", err)
	}
	ref := core.Ref{Owner: scope, Name: pkg}
	c := NewChain(core.Home())
	var got []string
	c.Sink = sinkFunc(func(r chain.Resolution) { got = append(got, r.String()) })
	t := time.Now()
	if _, err := c.Revisions(ctx, ref); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fail("%s: not found (looked in: %s)", args[0], strings.Join(c.Places(ref), ", "))
		}
		return fail("%v", err)
	}
	for _, g := range got {
		fmt.Printf("%s [%s]\n", g, time.Since(t).Round(time.Millisecond))
	}
	return 0
}

type sinkFunc func(chain.Resolution)

func (f sinkFunc) Resolved(r chain.Resolution) { f(r) }

// AfterInstall prints where private packages came from and, after a plain
// install, updates locally registered dependencies whose folder changed
// since the lockfile was written (npm keeps lockfile versions otherwise).
func AfterInstall(ctx context.Context, cfg daemon.Config, a core.Adapter, base string, args []string, start time.Time) {
	for _, l := range daemon.ResolutionsSince(cfg, start) {
		fmt.Fprintln(os.Stderr, "consona: source:", l)
	}
	if len(args) > 1 && (args[0] == "install" || args[0] == "i") {
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				return // explicit packages: npm did what was asked
			}
		}
	}
	stale := staleLocal(".")
	if len(stale) == 0 {
		return
	}
	names := make([]string, 0, len(stale))
	for n := range stale {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, "consona: local folder changed for %s: npm update\n", strings.Join(names, ", "))
	t := time.Now()
	a.Exec(ctx, base, append([]string{"update"}, names...))
	for _, l := range daemon.ResolutionsSince(cfg, t) {
		fmt.Fprintln(os.Stderr, "consona: source:", l)
	}
	for n, want := range staleLocal(".") {
		fmt.Fprintf(os.Stderr, "consona: %s is still at an older build (package.json range does not allow %s); run `consona install %s@latest`\n", n, want, n)
	}
}

// staleLocal maps registered package names in ./package-lock.json whose
// locked version differs from the folder's current version to the latter.
func staleLocal(dir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return nil
	}
	idx := localindex.Open()
	es, _ := idx.List()
	if len(es) == 0 {
		return nil
	}
	src := local.New(idx, core.Home())
	out := map[string]string{}
	for _, e := range es {
		for k, p := range lock.Packages {
			if !strings.EqualFold(k, "node_modules/"+e.Name) && !strings.HasSuffix(strings.ToLower(k), "/node_modules/"+e.Name) {
				continue
			}
			scope, pkg, _ := localindex.SplitName(e.Name)
			c, err := src.Resolve(core.Ref{Owner: scope, Name: pkg})
			if err == nil && c.Version != p.Version {
				out[e.Name] = c.Version
			}
		}
	}
	return out
}
