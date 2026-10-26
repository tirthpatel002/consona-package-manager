package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// parsePMFlag strips --pm <value> and --pm=<value> from args (before --).
func parsePMFlag(args []string) (string, []string, error) {
	var pm string
	clean := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			clean = append(clean, args[i:]...)
			break
		}
		if a == "--pm" {
			if i+1 >= len(args) {
				return "", nil, errors.New("flag --pm requires an argument (npm or pnpm)")
			}
			pm = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--pm=") {
			pm = strings.TrimPrefix(a, "--pm=")
			continue
		}
		clean = append(clean, a)
	}
	return pm, clean, nil
}

// detectPM determines whether to run npm or pnpm for an install/audit command.
// Precedence:
//  1. --pm flag (npm|pnpm)
//  2. CONSONA_PM environment variable
//  3. package.json "packageManager" field (e.g. "pnpm@9.12.0" -> pnpm, "npm@..." -> npm)
//  4. pnpm-lock.yaml present in dir -> pnpm
//  5. default -> npm
func detectPM(args []string, dir string) (pm, reason string, cleanArgs []string, err error) {
	flagVal, clean, err := parsePMFlag(args)
	if err != nil {
		return "", "", nil, err
	}
	cleanArgs = clean

	if flagVal != "" {
		pm = strings.ToLower(strings.TrimSpace(flagVal))
		if pm != "npm" && pm != "pnpm" {
			return "", "", nil, fmt.Errorf("unknown package manager %q: consona supports npm and pnpm", flagVal)
		}
		return pm, "--pm flag", cleanArgs, nil
	}

	if envVal := strings.TrimSpace(os.Getenv("CONSONA_PM")); envVal != "" {
		pm = strings.ToLower(envVal)
		if pm != "npm" && pm != "pnpm" {
			return "", "", nil, fmt.Errorf("unknown CONSONA_PM %q: consona supports npm and pnpm", envVal)
		}
		return pm, "CONSONA_PM env", cleanArgs, nil
	}

	pjPath := filepath.Join(dir, "package.json")
	if data, err := os.ReadFile(pjPath); err == nil {
		var pj struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(data, &pj) == nil && pj.PackageManager != "" {
			name := strings.ToLower(strings.TrimSpace(pj.PackageManager))
			if i := strings.Index(name, "@"); i >= 0 {
				name = name[:i]
			}
			if name == "pnpm" {
				return "pnpm", "package.json packageManager", cleanArgs, nil
			}
			if name == "npm" {
				return "npm", "package.json packageManager", cleanArgs, nil
			}
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return "pnpm", "pnpm-lock.yaml", cleanArgs, nil
	}

	return "npm", "default", cleanArgs, nil
}

// checkPMInstalled verifies that the chosen package manager is available on PATH.
func checkPMInstalled(pm string) error {
	if pm == "pnpm" {
		if _, err := exec.LookPath("pnpm"); err != nil {
			return errors.New("pnpm not found; install it (corepack enable / npm i -g pnpm) or use --pm npm")
		}
	}
	return nil
}

// pnpmArgs formats arguments for running pnpm install or audit with equivalent
// live verbose output when not quiet, or default quiet output.
func pnpmArgs(args []string, quiet bool) []string {
	if quiet {
		return args
	}
	out := make([]string, len(args))
	copy(out, args)
	if !hasPnpmReporter(out) {
		out = append(out, "--reporter=append-only")
	}
	if !hasPnpmLoglevel(out) && os.Getenv("npm_config_loglevel") == "" && os.Getenv("NPM_CONFIG_LOGLEVEL") == "" {
		out = append(out, "--loglevel=info")
	}
	return out
}

func hasPnpmReporter(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--reporter" || strings.HasPrefix(a, "--reporter=") {
			return true
		}
	}
	return false
}

func hasPnpmLoglevel(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		switch {
		case a == "--loglevel", strings.HasPrefix(a, "--loglevel="),
			a == "--silent", a == "-s", a == "-d", a == "-dd", a == "-ddd":
			return true
		}
	}
	return false
}
