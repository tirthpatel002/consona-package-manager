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
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"

	_ "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm" // adapters register themselves
)

const usage = `consona: private packages from your GitHub repos, zero repo setup.

  consona setup [--scope @owner ...] [--no-autostart]
                                       one-time: point npm at consona (private scopes from GitHub,
                                       everything else via your previous registry, with your login)
                                       and start the daemon at login (systemd/launchd/logon task)
  consona setup --undo                 restore your previous npm settings, remove auto-start (alias: uninstall)
  consona install [npm args...]        npm install (starts the daemon if needed)
  consona audit [npm args...]          npm audit (starts the daemon if needed)
  consona daemon start|stop|status     manage the local daemon
  consona version                      print the version

After setup every npm command asks consona first. If the daemon is not running,
npm fails within seconds with ECONNREFUSED 127.0.0.1:<port> (it never falls back
to the public registry): run any consona command, e.g. "consona daemon start".
`

// version is set by the release build (-ldflags "-X main.version=...").
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "consona: "+format+"\n", a...)
	return 1
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cfg := daemon.Load()
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
				path, err := npm.Teardown(ctx)
				if err != nil {
					return fail("%v", err)
				}
				if err := daemon.RemoveAutostart(cfg); err != nil {
					return fail("removing auto-start: %v", err)
				}
				daemon.Stop(cfg)
				fmt.Printf("consona: removed consona settings from %s (npm uses your previous registry again), removed auto-start, stopped the daemon\n", path)
				return 0
			case args[i] == "--no-autostart":
				noAutostart = true
			case args[i] == "--scope" && i+1 < len(args):
				i++
				extra = append(extra, args[i])
			case strings.HasPrefix(args[i], "--scope="):
				extra = append(extra, strings.TrimPrefix(args[i], "--scope="))
			default:
				return fail("unknown setup argument %q", args[i])
			}
		}
		src, err := github.New()
		if err != nil {
			return fail("%v", err)
		}
		owners, err := src.Owners(ctx)
		if err != nil {
			return fail("%v", err)
		}
		owners = append(owners, extra...)
		path, err := npm.Setup(ctx, npmBase, owners)
		if err != nil {
			return fail("%v", err)
		}
		auto := "auto-start: off (--no-autostart)"
		if !noAutostart {
			unit, err := daemon.InstallAutostart(cfg)
			if err != nil {
				return fail("registering auto-start: %v (rerun with --no-autostart to skip)", err)
			}
			auto = "auto-start at login + restart on crash: " + unit
			if os.Getenv("GH_TOKEN")+os.Getenv("GITHUB_TOKEN") != "" {
				auto += "\nconsona: note: the auto-started daemon does not see GH_TOKEN/GITHUB_TOKEN; it uses your `gh auth login`"
			}
		}
		if err := daemon.Ensure(cfg); err != nil {
			return fail("%v", err)
		}
		fmt.Println("consona: " + auto)
		fmt.Printf("consona: npm now asks %s first (%s): @%s served from GitHub, everything else goes to your previous registry\n",
			npmBase, path, strings.ToLower(strings.Join(owners, ", @")))
		return 0
	case "install", "i", "audit":
		if err := npm.CheckSetup(npmBase); err != nil {
			return fail("%v", err)
		}
		if err := daemon.Ensure(cfg); err != nil {
			return fail("%v", err)
		}
		start := time.Now()
		cmdName := "install"
		if args[0] == "audit" {
			cmdName = "audit"
		}
		code := npm.Exec(ctx, npmBase, append([]string{cmdName}, args[1:]...))
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
			src, err := github.New()
			if err != nil {
				log.Print(err)
				return 1
			}
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
			if err := daemon.Ensure(cfg); err != nil {
				return fail("%v", err)
			}
			fmt.Printf("consona: daemon running on %s (log %s)\n", cfg.Addr(), cfg.LogPath())
			return 0
		}
	case "version", "--version":
		fmt.Println("consona " + version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprint(os.Stderr, usage)
	return 2
}
