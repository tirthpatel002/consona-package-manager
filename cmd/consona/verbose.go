package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

// Live progress: every command says what it is doing on stderr, as it
// happens. `--quiet` (anywhere before `--`) or CONSONA_QUIET=1 turns it
// off. Everything printed goes through redact, and structured events are
// redacted before they are logged, so no token value is ever shown.

var (
	quiet bool
	vmu   sync.Mutex
	vout  io.Writer = os.Stderr
)

// envQuiet reports whether CONSONA_QUIET asks for quiet output.
func envQuiet() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CONSONA_QUIET"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// parseQuiet removes consona's --quiet flag from args (not after `--`)
// and reports whether output should be quiet.
func parseQuiet(args []string) ([]string, bool) {
	q := envQuiet()
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "--quiet" {
			q = true
			continue
		}
		out = append(out, a)
	}
	return out, q
}

// say prints one progress line (unless quiet).
func say(format string, a ...any) {
	if quiet {
		return
	}
	msg := redact.String(fmt.Sprintf(format, a...))
	vmu.Lock()
	defer vmu.Unlock()
	fmt.Fprintln(vout, "consona: "+msg)
}

// sayEvent prints a structured log event (already redacted by logstore).
func sayEvent(e logstore.Entry) {
	if quiet || e.Level == "debug" {
		return
	}
	if e.Event == logstore.EvBuildCacheRemoteHit {
		remote, _ := e.Fields["remote"].(string)
		if remote == "" {
			remote = "remote"
		}
		tookStr, _ := e.Fields["took"].(string)
		if tookStr == "" {
			tookStr = fmt.Sprintf("%.1fs", float64(e.DurationMS)/1000.0)
		}
		say("build cache: got %s@%s (%s) from ssh remote %s in %s", e.Package, e.Version, shortCommit(e.CommitSHA), remote, tookStr)
		return
	}
	say("%s", logstore.Human(e))
}

// followDaemon prints the daemon's events (credential used, where each
// package resolved from, builds, warnings, errors) live until stop is
// called; stop prints whatever arrived last.
func followDaemon(cfg daemon.Config, since time.Time) (stop func()) {
	if quiet {
		return func() {}
	}
	last := since
	poll := func() {
		for _, e := range daemon.EventsSince(cfg, last) {
			sayEvent(e)
			if e.TS.After(last) {
				last = e.TS
			}
		}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				time.Sleep(150 * time.Millisecond) // the daemon logs a request just after answering it
				poll()
				return
			case <-t.C:
				poll()
			}
		}
	}()
	return func() { close(done); <-finished }
}

// ensureDaemon is daemon.Ensure with progress lines (health, start).
func ensureDaemon(cfg daemon.Config) error {
	up, current := daemon.Health(cfg)
	switch {
	case current:
		say("daemon healthy on %s", cfg.Addr())
		return nil
	case up:
		say("daemon on %s is another consona build or home: restarting it", cfg.Addr())
	default:
		say("daemon not running on %s: starting it", cfg.Addr())
	}
	if daemon.AutostartInstalled(cfg) {
		say("starting it through the login service (auto-start)")
	}
	t := time.Now()
	if err := daemon.Ensure(cfg); err != nil {
		return err
	}
	say("daemon up on %s in %s (log %s)", cfg.Addr(), time.Since(t).Round(time.Millisecond), cfg.LogPath())
	return nil
}

// sayNpmrcDiff prints the npmrc lines a setup or undo wrote and removed.
// Credential lines are shown by key only.
func sayNpmrcDiff(path, before, after string) {
	if quiet {
		return
	}
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				m[l]++
			}
		}
		return m
	}
	b, a := count(before), count(after)
	show := func(sign, l string) {
		k, _, ok := strings.Cut(l, "=")
		if ok && isSecretKey(k) {
			l = strings.TrimSpace(k) + "=[redacted]"
		}
		say("  %s %s", sign, l)
	}
	changed := false
	for _, l := range strings.Split(before, "\n") {
		if l = strings.TrimSpace(l); l != "" && b[l] > a[l] {
			if !changed {
				say("npmrc %s:", path)
				changed = true
			}
			b[l]--
			show("-", l)
		}
	}
	b = count(before)
	for _, l := range strings.Split(after, "\n") {
		if l = strings.TrimSpace(l); l != "" && a[l] > b[l] {
			if !changed {
				say("npmrc %s:", path)
				changed = true
			}
			a[l]--
			show("+", l)
		}
	}
	if !changed {
		say("npmrc %s: no changes", path)
	}
}

func isSecretKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, s := range []string{"_authtoken", "_auth", "_password", "token", "certfile", "keyfile"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// npmArgs adds --verbose to an npm install/audit unless output is quiet or
// the user already chose a log level. When quiet and the npmrc's
// loglevel=verbose is consona's, npm runs at its default level instead.
func npmArgs(args []string, quiet, consonaLoglevel bool) []string {
	if hasLoglevel(args) || os.Getenv("npm_config_loglevel") != "" || os.Getenv("NPM_CONFIG_LOGLEVEL") != "" {
		return args
	}
	if !quiet {
		return append(args, "--verbose")
	}
	if consonaLoglevel {
		return append(args, "--loglevel=notice")
	}
	return args
}

func hasLoglevel(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		switch {
		case a == "--verbose", a == "--quiet", a == "--silent", a == "-s", a == "-q", a == "-d", a == "-dd", a == "-ddd",
			a == "--loglevel", strings.HasPrefix(a, "--loglevel="):
			return true
		}
	}
	return false
}
