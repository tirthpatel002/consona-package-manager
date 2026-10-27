package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPM(t *testing.T) {
	tmp := t.TempDir()

	// 1. Default (no package.json, no lockfile, no env, no flags)
	pm, reason, clean, err := detectPM([]string{"install"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "npm" || reason != "default" {
		t.Errorf("got pm=%q, reason=%q; want npm, default", pm, reason)
	}
	if len(clean) != 1 || clean[0] != "install" {
		t.Errorf("got clean=%v; want [install]", clean)
	}

	// 2. pnpm-lock.yaml present
	lockPath := filepath.Join(tmp, "pnpm-lock.yaml")
	if err := os.WriteFile(lockPath, []byte("lockfileVersion: '9.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pm, reason, _, err = detectPM([]string{"install"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "pnpm" || reason != "pnpm-lock.yaml" {
		t.Errorf("got pm=%q, reason=%q; want pnpm, pnpm-lock.yaml", pm, reason)
	}

	// 3. package.json with packageManager field overrides pnpm-lock.yaml
	pjPath := filepath.Join(tmp, "package.json")
	if err := os.WriteFile(pjPath, []byte(`{"name":"test","packageManager":"npm@10.8.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pm, reason, _, err = detectPM([]string{"install"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "npm" || reason != "package.json packageManager" {
		t.Errorf("got pm=%q, reason=%q; want npm, package.json packageManager", pm, reason)
	}

	// 4. package.json with pnpm variants
	for _, variant := range []string{"pnpm@9.12.0", "pnpm@8.0.0+sha512.abcdef", "pnpm"} {
		if err := os.WriteFile(pjPath, []byte(`{"packageManager":"`+variant+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		pm, reason, _, err = detectPM([]string{"install"}, tmp)
		if err != nil {
			t.Fatalf("variant %q: unexpected error: %v", variant, err)
		}
		if pm != "pnpm" || reason != "package.json packageManager" {
			t.Errorf("variant %q: got pm=%q, reason=%q; want pnpm, package.json packageManager", variant, pm, reason)
		}
	}

	// 5. CONSONA_PM env var overrides package.json and lockfile
	t.Setenv("CONSONA_PM", "npm")
	pm, reason, _, err = detectPM([]string{"install"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "npm" || reason != "CONSONA_PM env" {
		t.Errorf("got pm=%q, reason=%q; want npm, CONSONA_PM env", pm, reason)
	}

	t.Setenv("CONSONA_PM", "pnpm")
	pm, reason, _, err = detectPM([]string{"install"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "pnpm" || reason != "CONSONA_PM env" {
		t.Errorf("got pm=%q, reason=%q; want pnpm, CONSONA_PM env", pm, reason)
	}

	// Invalid CONSONA_PM
	t.Setenv("CONSONA_PM", "yarn")
	_, _, _, err = detectPM([]string{"install"}, tmp)
	if err == nil {
		t.Error("expected error for CONSONA_PM=yarn")
	}

	// 6. --pm flag overrides env, package.json, and lockfile
	t.Setenv("CONSONA_PM", "npm")
	pm, reason, clean, err = detectPM([]string{"install", "--pm", "pnpm", "pkg-a"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "pnpm" || reason != "--pm flag" {
		t.Errorf("got pm=%q, reason=%q; want pnpm, --pm flag", pm, reason)
	}
	if len(clean) != 2 || clean[0] != "install" || clean[1] != "pkg-a" {
		t.Errorf("got clean=%v; want [install pkg-a]", clean)
	}

	// --pm=npm syntax
	t.Setenv("CONSONA_PM", "pnpm")
	pm, reason, clean, err = detectPM([]string{"audit", "--pm=npm"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm != "npm" || reason != "--pm flag" {
		t.Errorf("got pm=%q, reason=%q; want npm, --pm flag", pm, reason)
	}
	if len(clean) != 1 || clean[0] != "audit" {
		t.Errorf("got clean=%v; want [audit]", clean)
	}

	// Invalid --pm
	_, _, _, err = detectPM([]string{"install", "--pm", "yarn"}, tmp)
	if err == nil {
		t.Error("expected error for --pm yarn")
	}

	// Missing --pm value
	_, _, _, err = detectPM([]string{"install", "--pm"}, tmp)
	if err == nil {
		t.Error("expected error for --pm without value")
	}

	// --pm after -- is not consumed
	t.Setenv("CONSONA_PM", "")
	pm, _, clean, err = detectPM([]string{"install", "--", "--pm", "pnpm"}, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With pj having pnpm from earlier
	if len(clean) != 4 || clean[0] != "install" || clean[1] != "--" || clean[2] != "--pm" || clean[3] != "pnpm" {
		t.Errorf("got clean=%v; expected args after -- preserved", clean)
	}
}

func TestCheckPMInstalled(t *testing.T) {
	// npm should be installed on test host
	if err := checkPMInstalled("npm"); err != nil {
		t.Errorf("npm check failed: %v", err)
	}

	// Test missing pnpm with an empty PATH
	t.Setenv("PATH", t.TempDir())
	err := checkPMInstalled("pnpm")
	if err == nil {
		t.Fatal("expected error for missing pnpm")
	}
	want := "pnpm not found; install it (corepack enable / npm i -g pnpm) or use --pm npm"
	if err.Error() != want {
		t.Errorf("got error %q; want %q", err.Error(), want)
	}
}

func TestPnpmArgs(t *testing.T) {
	// Quiet mode: unchanged
	args := []string{"install", "@foo/bar"}
	got := pnpmArgs(args, true)
	if strings.Join(got, " ") != "install @foo/bar" {
		t.Errorf("quiet got %v; want %v", got, args)
	}

	// Not quiet: adds --reporter=append-only and --loglevel=info
	got = pnpmArgs(args, false)
	want := "install @foo/bar --reporter=append-only --loglevel=info"
	if strings.Join(got, " ") != want {
		t.Errorf("verbose got %v; want %s", got, want)
	}

	// User provided --reporter
	got = pnpmArgs([]string{"install", "--reporter=ndjson"}, false)
	if strings.Contains(strings.Join(got, " "), "--reporter=append-only") {
		t.Errorf("unexpected --reporter=append-only in %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "--loglevel=info") {
		t.Errorf("expected --loglevel=info in %v", got)
	}

	// User provided --loglevel
	got = pnpmArgs([]string{"install", "--loglevel=warn"}, false)
	if strings.Contains(strings.Join(got, " "), "--loglevel=info") {
		t.Errorf("unexpected --loglevel=info in %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "--reporter=append-only") {
		t.Errorf("expected --reporter=append-only in %v", got)
	}

	// User provided --silent or -s
	for _, flag := range []string{"--silent", "-s", "-d", "-dd"} {
		got = pnpmArgs([]string{"install", flag}, false)
		if strings.Contains(strings.Join(got, " "), "--loglevel=info") {
			t.Errorf("flag %q: unexpected --loglevel=info in %v", flag, got)
		}
	}

	// Env npm_config_loglevel inhibits default loglevel
	t.Setenv("npm_config_loglevel", "warn")
	got = pnpmArgs([]string{"install"}, false)
	if strings.Contains(strings.Join(got, " "), "--loglevel=info") {
		t.Errorf("unexpected --loglevel=info when npm_config_loglevel is set: %v", got)
	}
}
