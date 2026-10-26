package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/daemon"
	"github.com/tirthpatel002/consona-package-manager/internal/localcli"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

type ciFlags struct {
	scopes     []string
	port       int
	workingDir string
}

func parseCiArgs(args []string) (ciFlags, []string, error) {
	var flags ciFlags
	var extra []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			extra = append(extra, args[i:]...)
			break
		}
		switch {
		case a == "--scope" && i+1 < len(args):
			i++
			flags.scopes = append(flags.scopes, normScope(args[i]))
		case strings.HasPrefix(a, "--scope="):
			flags.scopes = append(flags.scopes, normScope(strings.TrimPrefix(a, "--scope=")))
		case a == "--port" && i+1 < len(args):
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil || p < 0 || p > 65535 {
				return flags, nil, fmt.Errorf("invalid port %q", args[i])
			}
			flags.port = p
		case strings.HasPrefix(a, "--port="):
			p, err := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if err != nil || p < 0 || p > 65535 {
				return flags, nil, fmt.Errorf("invalid port %q", strings.TrimPrefix(a, "--port="))
			}
			flags.port = p
		case a == "--prefix" && i+1 < len(args):
			flags.workingDir = args[i+1]
			extra = append(extra, a, args[i+1])
			i++
		case strings.HasPrefix(a, "--prefix="):
			flags.workingDir = strings.TrimPrefix(a, "--prefix=")
			extra = append(extra, a)
		case a == "help" || a == "-h" || a == "--help":
			fmt.Print(`consona ci: run npm ci with a temporary consona daemon for private packages.

Usage:
  consona ci [--scope @owner ...] [--port N] [npm ci args...]

Options:
  --scope @owner    Explicitly route private scope @owner (repeatable, also CONSONA_SCOPES)
  --port N          Run daemon on port N (default: port from package-lock.json or 48731)
  --quiet           Suppress consona stderr progress lines and verbose npm logs
`)
			return flags, nil, errHelp
		default:
			extra = append(extra, a)
		}
	}
	return flags, extra, nil
}

var errHelp = errors.New("help requested")

// isPortFree reports whether a TCP listener can bind to 127.0.0.1:port.
func isPortFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// findFreePort finds an available ephemeral TCP port on loopback.
func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// readLockfile extracts the port used in resolved URLs and all scoped owner names.
func readLockfile(path string) (scopes []string, port int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}

	scopeSet := map[string]bool{}

	checkResolved := func(resolved, pkgName string) {
		if resolved == "" {
			return
		}
		u, err := url.Parse(resolved)
		if err != nil {
			return
		}
		host, pStr, err := net.SplitHostPort(u.Host)
		if err != nil {
			return
		}
		if (host != "127.0.0.1" && host != "localhost") || !strings.HasPrefix(u.Path, "/npm/") {
			return
		}
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 && port == 0 {
			port = p
		}

		if s := scopeFromName(pkgName); s != "" {
			scopeSet[s] = true
		}

		unescapedPath, err := url.PathUnescape(u.Path)
		if err == nil {
			trimmed := strings.TrimPrefix(unescapedPath, "/npm/")
			if strings.HasPrefix(trimmed, "@") {
				parts := strings.SplitN(trimmed, "/", 2)
				if len(parts) > 0 {
					scopeSet[normScope(parts[0])] = true
				}
			}
		}
	}

	// 1. Structured JSON walk
	var doc struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if err := json.Unmarshal(b, &doc); err == nil {
		for pkgKey, pkg := range doc.Packages {
			checkResolved(pkg.Resolved, pkgKey)
		}
		walkDependencies(doc.Dependencies, checkResolved)
	}

	// 2. Regex fallback scan over raw bytes
	re := regexp.MustCompile(`https?://(?:127\.0\.0\.1|localhost):(\d+)/npm/(?:(?:@|%40)([^/]+)/)?`)
	for _, match := range re.FindAllStringSubmatch(string(b), -1) {
		if len(match) > 1 && port == 0 {
			if p, err := strconv.Atoi(match[1]); err == nil && p > 0 {
				port = p
			}
		}
		if len(match) > 2 && match[2] != "" {
			scopeSet[normScope(match[2])] = true
		}
	}

	for s := range scopeSet {
		if s != "" {
			scopes = append(scopes, s)
		}
	}
	sort.Strings(scopes)
	return scopes, port, nil
}

func walkDependencies(deps map[string]json.RawMessage, checkResolved func(resolved, name string)) {
	for name, raw := range deps {
		var dep struct {
			Resolved     string                     `json:"resolved"`
			Dependencies map[string]json.RawMessage `json:"dependencies"`
		}
		if err := json.Unmarshal(raw, &dep); err == nil {
			checkResolved(dep.Resolved, name)
			if len(dep.Dependencies) > 0 {
				walkDependencies(dep.Dependencies, checkResolved)
			}
		}
	}
}

func scopeFromName(name string) string {
	i := strings.LastIndex(name, "@")
	if i >= 0 {
		rest := name[i:]
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 && strings.HasPrefix(parts[0], "@") {
			return normScope(parts[0])
		}
	}
	return ""
}

// runCi executes the `consona ci` workflow for CI runners.
func runCi(ctx context.Context, initialCfg daemon.Config, args []string) int {
	start := time.Now()
	flags, extraNpmArgs, err := parseCiArgs(args)
	if errors.Is(err, errHelp) {
		return 0
	}
	if err != nil {
		return fail("%v", err)
	}

	dir := flags.workingDir
	if dir == "" {
		dir = "."
	}

	// Lockfile check early: package-lock.json or npm-shrinkwrap.json is required.
	lockfilePath := filepath.Join(dir, "package-lock.json")
	if _, err := os.Stat(lockfilePath); err != nil {
		shrinkPath := filepath.Join(dir, "npm-shrinkwrap.json")
		if _, err := os.Stat(shrinkPath); err == nil {
			lockfilePath = shrinkPath
		} else {
			fmt.Fprintln(os.Stderr, "consona: consona ci needs a package-lock.json; run consona install locally and commit the lockfile")
			logstore.Log(logstore.Entry{
				Event:      logstore.EvCIFinish,
				Level:      "error",
				Ecosystem:  "npm",
				Result:     "fail",
				DurationMS: time.Since(start).Milliseconds(),
				Message:    "consona ci needs a package-lock.json; run consona install locally and commit the lockfile",
			})
			return 1
		}
	}

	lockScopes, lockPort, err := readLockfile(lockfilePath)
	if err != nil {
		logstore.Log(logstore.Entry{
			Event:      logstore.EvCIFinish,
			Level:      "error",
			Ecosystem:  "npm",
			Result:     "fail",
			DurationMS: time.Since(start).Milliseconds(),
			Message:    fmt.Sprintf("reading %s: %v", filepath.Base(lockfilePath), err),
		})
		return fail("reading %s: %v", filepath.Base(lockfilePath), err)
	}

	// Discover scopes from lockfile, flags, env (CONSONA_SCOPES), and extra local owners.
	var scopes []string
	scopes = append(scopes, lockScopes...)
	scopes = append(scopes, flags.scopes...)
	if envScopes := os.Getenv("CONSONA_SCOPES"); envScopes != "" {
		for _, s := range strings.Split(envScopes, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, normScope(s))
			}
		}
	}
	scopes = dedupe(append(scopes, localcli.ExtraOwners()...))

	// Determine port and registry rewrite behavior:
	// npm's default `replace-registry-host` is "npmjs", which only rewrites registry.npmjs.org.
	// Setting `replace-registry-host=always` (or passing npm_config_replace_registry_host=always)
	// makes npm rewrite resolved loopback URLs to the configured registry.
	// If the preferred lockfile port (default 48731) is free, we bind directly to it.
	// If it is busy, we pick a free port and enable `replace-registry-host=always`.
	targetPort := lockPort
	if targetPort == 0 {
		targetPort = initialCfg.Port
		if targetPort == 0 {
			targetPort = 48731
		}
	}

	rewriteRegistryHost := false
	if flags.port > 0 {
		targetPort = flags.port
		if !isPortFree(targetPort) {
			logstore.Log(logstore.Entry{
				Event:      logstore.EvCIFinish,
				Level:      "error",
				Ecosystem:  "npm",
				Result:     "fail",
				DurationMS: time.Since(start).Milliseconds(),
				Message:    fmt.Sprintf("port %d is already in use; free the port or pass another --port", targetPort),
			})
			return fail("port %d is already in use; free the port or pass another --port", targetPort)
		}
		if lockPort != 0 && targetPort != lockPort {
			rewriteRegistryHost = true
		}
	} else {
		if isPortFree(targetPort) {
			// Preferred port is free; use it without rewriting.
		} else {
			// Port is busy: allocate a free port and enable npm host rewriting.
			freePort, err := findFreePort()
			if err != nil {
				return fail("cannot find free port: %v", err)
			}
			say("port %d from %s is busy; using free port %d with npm replace-registry-host",
				targetPort, filepath.Base(lockfilePath), freePort)
			targetPort = freePort
			rewriteRegistryHost = true
		}
	}

	// CONSONA_HOME: use explicitly set CONSONA_HOME if provided, else create a temp dir.
	var tempHome string
	createdTempHome := false
	if envHome := os.Getenv("CONSONA_HOME"); envHome != "" {
		tempHome = envHome
	} else {
		th, err := os.MkdirTemp("", "consona-ci-home-*")
		if err != nil {
			return fail("creating temp home: %v", err)
		}
		tempHome = th
		createdTempHome = true
		_ = os.Setenv("CONSONA_HOME", tempHome)
	}

	// Temp npmrc directory: holds temporary .npmrc for this child run only.
	tempDir, err := os.MkdirTemp("", "consona-ci-npmrc-*")
	if err != nil {
		if createdTempHome {
			_ = os.RemoveAll(tempHome)
		}
		return fail("creating temp npmrc dir: %v", err)
	}
	tempNpmrc := filepath.Join(tempDir, ".npmrc")

	cfg := daemon.Config{Port: targetPort, Home: tempHome}
	var daemonStarted bool
	daemonErrCh := make(chan error, 1)

	// ALWAYS clean up: stop the daemon and remove temp directories on success, failure, panic, or signals.
	defer func() {
		if daemonStarted {
			say("stopping daemon on %s", cfg.Addr())
			daemon.Stop(cfg)
			select {
			case <-daemonErrCh:
			case <-time.After(2 * time.Second):
			}
		}
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		if createdTempHome && tempHome != "" {
			_ = os.RemoveAll(tempHome)
		}
	}()

	// Write temp npmrc using the adapter's npmrc generator.
	baseURL := cfg.BaseURL() + "/npm/"
	var npmrcContent string
	if quiet {
		npmrcContent, err = npmadapter.UpdateNpmrc("", baseURL, scopes)
	} else {
		npmrcContent, _, err = npmadapter.SetupNpmrc("", baseURL, scopes, false)
	}
	if err != nil {
		return fail("generating temp npmrc: %v", err)
	}
	if rewriteRegistryHost {
		npmrcContent += "replace-registry-host=always\n"
	}
	if err := os.WriteFile(tempNpmrc, []byte(npmrcContent), 0o600); err != nil {
		return fail("writing temp npmrc: %v", err)
	}

	// Start consona daemon in-process in a goroutine.
	src := localcli.NewDaemonSource(cfg.Home)
	go func() {
		daemonErrCh <- daemon.Serve(cfg, src)
	}()
	daemonStarted = true

	// Wait for daemon to be ready.
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-daemonErrCh:
			return fail("daemon failed to start: %v", err)
		default:
		}
		if up, _ := daemon.Health(cfg); up {
			ready = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !ready {
		return fail("daemon did not start in time on %s", cfg.Addr())
	}
	say("daemon running on %s (temp home %s)", cfg.Addr(), cfg.Home)

	logstore.Log(logstore.Entry{
		Event:     logstore.EvCIStart,
		Level:     "info",
		Ecosystem: "npm",
		Result:    "ok",
		Message:   fmt.Sprintf("starting ci run on port %d with scopes @%s", targetPort, strings.Join(scopes, ", @")),
		Fields: map[string]any{
			"port":    targetPort,
			"scopes":  scopes,
			"rewrite": rewriteRegistryHost,
		},
	})

	// Prepare npm ci command arguments.
	npmCmd := npmArgs(append([]string{"ci"}, extraNpmArgs...), quiet, true)
	say("running: npm %s", strings.Join(npmCmd, " "))

	childEnv := os.Environ()
	childEnv = append(childEnv,
		"npm_config_userconfig="+tempNpmrc,
		"npm_config_registry="+baseURL,
		"CONSONA_HOME="+tempHome,
		"CONSONA_PORT="+strconv.Itoa(targetPort),
	)
	if rewriteRegistryHost {
		childEnv = append(childEnv, "npm_config_replace_registry_host=always")
	}

	stopFollow := followDaemon(cfg, start)

	cmd := exec.CommandContext(ctx, "npm", npmCmd...)
	if dir != "" && dir != "." {
		cmd.Dir = dir
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnv

	if err := cmd.Start(); err != nil {
		stopFollow()
		say("cannot run npm: %v", err)
		logstore.Log(logstore.Entry{
			Event:      logstore.EvCIFinish,
			Level:      "error",
			Ecosystem:  "npm",
			Result:     "fail",
			DurationMS: time.Since(start).Milliseconds(),
			Message:    fmt.Sprintf("cannot run npm: %v", err),
		})
		return 127
	}

	// Trap SIGINT and SIGTERM: forward to child npm, wait for it to exit, then proceed to cleanup.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)

	cmdDone := make(chan struct{})
	go func() {
		select {
		case sig := <-sigc:
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		case <-cmdDone:
		}
	}()

	err = cmd.Wait()
	close(cmdDone)
	stopFollow()

	exitCode := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
		exitCode = 0
	case errors.As(err, &ee):
		exitCode = ee.ExitCode()
		if exitCode < 0 {
			if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				exitCode = 128 + int(status.Signal())
			} else {
				exitCode = 1
			}
		}
	default:
		say("npm ci failed: %v", err)
		exitCode = 1
	}

	for _, m := range daemon.WarningsSince(cfg, start) {
		fmt.Fprintln(os.Stderr, "consona: warning:", m)
	}
	if exitCode != 0 {
		for _, m := range daemon.ErrorsSince(cfg, start) {
			fmt.Fprintln(os.Stderr, "consona:", m)
		}
	}

	res := "ok"
	lvl := "info"
	if exitCode != 0 {
		res = "fail"
		lvl = "error"
	}
	logstore.Log(logstore.Entry{
		Event:      logstore.EvCIFinish,
		Level:      lvl,
		Ecosystem:  "npm",
		Result:     res,
		DurationMS: time.Since(start).Milliseconds(),
		Message:    fmt.Sprintf("npm ci exited with code %d", exitCode),
	})

	say("npm ci exited with code %d after %s", exitCode, time.Since(start).Round(time.Millisecond))
	return exitCode
}
