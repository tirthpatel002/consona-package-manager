// Command consona serves private packages straight from GitHub repos to
// native package tools. See README.md.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/localcli"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm" // adapters register themselves
)

const usage = `consona: private packages from your GitHub repos, zero repo setup.

  consona setup [--scope @owner ...] [--no-autostart]
                                       one-time: point npm at consona (private scopes from GitHub,
                                       everything else via your previous registry, with your login)
                                       and start the daemon at login (systemd/launchd/logon task)
  consona setup --undo                 restore your previous npm settings, remove auto-start (alias: uninstall)
  consona install [npm args...]        npm install (starts the daemon if needed)
  consona audit [npm args...]          npm audit (starts the daemon if needed)
  consona ci [npm args...]             ci runners: temporary daemon + npm ci
  consona daemon start|stop|status     manage the local daemon
  consona token add|list|test|remove   optional named GitHub tokens (see "consona token help")
  consona logs [filters] | export | prune | clear | info
                                       structured event log (see "consona logs help")
  consona version                      print the version

Every command prints what it is doing on stderr (npm config lines, credential used, daemon
health, where each package came from, warnings). --quiet or CONSONA_QUIET=1 turns that off;
install and audit then also run npm without --verbose.

GitHub credentials (any one is enough; gh is supported but not required):
per-owner env CONSONA_TOKEN_<OWNER>, named tokens ("consona token add"),
CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN, a default named token, the gh login.
"consona token help" shows the full order.

After setup every npm command asks consona first. If the daemon is not running,
npm fails within seconds with ECONNREFUSED 127.0.0.1:<port> (it never falls back
to the public registry): run any consona command, e.g. "consona daemon start".
`

// version is set by the release build (-ldflags "-X main.version=...").
var version = "0.10.0"

func main() { os.Exit(run(os.Args[1:])) }

// logsEvents are the commands that write to the event log. Read-only and
// informational commands (version, help, whoami, logs, ...) never create
// the log store.
var logsEvents = map[string]bool{"setup": true, "uninstall": true, "install": true, "i": true, "audit": true,
	"token": true, "tokens": true, "init": true, "register": true, "unregister": true, "remote": true,
	"remote-serve": true, "which": true, "ci": true}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "consona: "+format+"\n", a...)
	return 1
}

func run(args []string) int {
	args, quiet = parseQuiet(args)
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage+localcli.Usage)
		return 2
	}
	localcli.Version = version
	cfg := daemon.Load()
	if args[0] != "daemon" || (len(args) > 1 && args[1] != "run" && args[1] != "supervise") {
		defer logstore.AddTee(sayEvent)() // this process's own events, live
	}
	if logsEvents[args[0]] { // commands that record events (the daemon opens its own)
		if logs := daemon.OpenLogs(cfg, false); logs != nil {
			defer logs.Close()
		}
	}
	if code, ok := localcli.Run(args); ok { // init, register, remote, ...
		return code
	}
	ctx := context.Background()
	npm, _ := core.Lookup("npm")
	npmBase := cfg.BaseURL() + "/npm/"
	switch args[0] {
	case "uninstall":
		args = []string{"setup", "--undo"}
		fallthrough
	case "setup":
		var extra []string
		noAutostart := false
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--undo":
				before := readNpmrc()
				say("removing consona's lines from the npm config")
				path, err := npm.Teardown(ctx)
				if err != nil {
					return fail("%v", err)
				}
				sayNpmrcDiff(path, before, readNpmrc())
				say("removing auto-start")
				if err := daemon.RemoveAutostart(cfg); err != nil {
					return fail("removing auto-start: %v", err)
				}
				say("stopping the daemon on %s", cfg.Addr())
				daemon.Stop(cfg)
				fmt.Printf("consona: removed consona settings from %s (npm uses your previous registry again), removed auto-start, stopped the daemon\n", path)
				return 0
			case args[i] == "--no-autostart":
				noAutostart = true
			case args[i] == "--scope" && i+1 < len(args):
				i++
				extra = append(extra, normScope(args[i]))
			case strings.HasPrefix(args[i], "--scope="):
				extra = append(extra, normScope(strings.TrimPrefix(args[i], "--scope=")))
			default:
				return fail("unknown setup argument %q", args[i])
			}
		}
		src := github.New()
		say("finding your GitHub owners (credential order: CONSONA_TOKEN_<OWNER>, named tokens, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN, gh login)")
		owners, err := src.Owners(ctx)
		if err == nil {
			say("GitHub owners: @%s", strings.Join(owners, ", @"))
		}
		local := localcli.ExtraOwners() // this device's username, registered and remote scopes
		if len(local) > 0 {
			say("this device's scopes (username, registered folders, ssh remotes): @%s", strings.Join(local, ", @"))
		}
		if err != nil {
			if len(extra) == 0 && len(local) == 0 { // nothing to serve without GitHub
				return fail("%v", err)
			}
			if len(extra) > 0 {
				fmt.Fprintf(os.Stderr, "consona: warning: could not discover your GitHub owners, routing only --scope owners:\n%v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "consona: GitHub not available (%v): serving this device's folders and ssh remotes only\n", err)
			}
		}
		owners = dedupe(append(append(owners, extra...), local...))
		before := readNpmrc()
		say("writing the npm config (registry %s, private scopes, retry timeouts, loglevel=verbose unless you set a loglevel)", npmBase)
		path, err := npm.Setup(ctx, npmBase, owners)
		if err != nil {
			return fail("%v", err)
		}
		sayNpmrcDiff(path, before, readNpmrc())
		auto := "auto-start: off (--no-autostart)"
		if !noAutostart {
			say("installing auto-start (start at login, restart on crash)")
			unit, err := daemon.InstallAutostart(cfg)
			if err != nil {
				return fail("registering auto-start: %v (rerun with --no-autostart to skip)", err)
			}
			say("auto-start installed: %s", unit)
			auto = "auto-start at login + restart on crash: " + unit
			if vars := tokenEnvVars(); len(vars) > 0 {
				auto += "\nconsona: note: the auto-started daemon does not see " + strings.Join(vars, ", ") +
					" from this shell; it uses named tokens (`consona token add`) and the gh login. Use --no-autostart to keep using env vars."
			}
		}
		if err := ensureDaemon(cfg); err != nil {
			return fail("%v", err)
		}
		fmt.Println("consona: " + auto)
		fmt.Printf("consona: npm now asks %s first (%s): @%s served by consona (this device's folders, ssh remotes, GitHub), everything else goes to your previous registry\n",
			npmBase, path, strings.ToLower(strings.Join(owners, ", @")))
		logstore.Log(logstore.Entry{Event: logstore.EvSetup, Level: "info", Ecosystem: "npm", Result: "ok",
			Message: "routed @" + strings.ToLower(strings.Join(owners, ", @"))})
		return 0
	case "token", "tokens":
		return newTokenCmd().run(ctx, args[1:])
	case "logs", "log":
		return runLogs(cfg, args[1:])
	case "ci":
		return runCi(ctx, cfg, args[1:])
	case "install", "i", "audit":
		if err := npm.CheckSetup(npmBase); err != nil {
			return fail("%v", err)
		}
		say("npm is set up for consona (%s)", npmadapter.UserConfig())
		if err := ensureDaemon(cfg); err != nil {
			return fail("%v", err)
		}
		start := time.Now()
		cmdName := "install"
		if args[0] == "audit" {
			cmdName = "audit"
		}
		npmCmd := npmArgs(append([]string{cmdName}, args[1:]...), quiet, npmadapter.OwnsLoglevel())
		say("running: npm %s (private scopes via %s, everything else via %s)", strings.Join(npmCmd, " "), npmBase, npmadapter.Upstream())
		stopFollow := followDaemon(cfg, start)
		code := npm.Exec(ctx, npmBase, npmCmd)
		stopFollow()
		say("npm %s exited with code %d after %s", cmdName, code, time.Since(start).Round(time.Millisecond))
		if code == 0 && cmdName == "install" {
			localcli.AfterInstall(ctx, cfg, npm, npmBase, append([]string{cmdName}, args[1:]...), start)
		}
		for _, m := range daemon.WarningsSince(cfg, start) {
			fmt.Fprintln(os.Stderr, "consona: warning:", m)
		}
		if code != 0 {
			for _, m := range daemon.ErrorsSince(cfg, start) {
				fmt.Fprintln(os.Stderr, "consona:", m)
			}
		}
		return code
	case "daemon":
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		switch sub {
		case "run":
			src := localcli.NewDaemonSource(cfg.Home) // local folders, ssh remotes, GitHub
			if err := daemon.Serve(cfg, src); err != nil {
				log.Print(err)
				return 1
			}
			return 0
		case "supervise":
			return daemon.Supervise(cfg)
		case "stop":
			if daemon.Stop(cfg) {
				fmt.Println("consona: daemon stopped")
			}
			return 0
		case "start", "status":
			if err := ensureDaemon(cfg); err != nil {
				return fail("%v", err)
			}
			fmt.Printf("consona: daemon running on %s (log %s)\n", cfg.Addr(), cfg.LogPath())
			return 0
		}
	case "version", "--version":
		fmt.Println("consona " + version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage + localcli.Usage)
		return 0
	}
	fmt.Fprint(os.Stderr, usage+localcli.Usage)
	return 2
}

// normScope strips any leading '@'s (`--scope @acme` and `--scope acme`
// mean the same; output adds one '@').
func normScope(s string) string { return creds.NormOwner(s) }

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = normScope(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// tokenEnvVars lists credential env vars set in this shell (names only).
func tokenEnvVars() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if v != "" && (k == "GH_TOKEN" || k == "GITHUB_TOKEN" || k == "CONSONA_TOKEN" || strings.HasPrefix(k, "CONSONA_TOKEN_")) {
			out = append(out, k)
		}
	}
	return out
}

// readNpmrc is the user npmrc's content ("" if missing).
func readNpmrc() string {
	b, _ := os.ReadFile(npmadapter.UserConfig())
	return string(b)
}
