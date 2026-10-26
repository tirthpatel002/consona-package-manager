package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	npmadapter "github.com/tirthpatel002/consona-package-manager/internal/adapter/npm"
	"github.com/tirthpatel002/consona-package-manager/internal/lookalike"
)

func setupTempProject(t *testing.T, pkgJSON string, lockJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if lockJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lockJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func setupTestNpmrc(t *testing.T, scopes ...string) {
	t.Helper()
	npmrcPath := filepath.Join(t.TempDir(), ".npmrc")
	t.Setenv("npm_config_userconfig", npmrcPath)

	var sb strings.Builder
	sb.WriteString("# consona block\nregistry=http://127.0.0.1:48731/npm\n")
	for _, s := range scopes {
		sb.WriteString("@" + s + ":registry=http://127.0.0.1:48731/npm\n")
	}
	if err := os.WriteFile(npmrcPath, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCommandOverTempProject(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	setupTestNpmrc(t, "acme")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/@acme%2futils":
			w.WriteHeader(http.StatusOK) // lookalike exists!
		case "/@acme%2fcore":
			w.WriteHeader(http.StatusNotFound) // ok
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	pkgJSON := `{
		"name": "my-app",
		"dependencies": {
			"@acme/utils": "^1.0.0",
			"lodash": "^4.17.21"
		},
		"devDependencies": {
			"@acme/core": "^2.0.0"
		}
	}`
	dir := setupTempProject(t, pkgJSON, "")

	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1 (lookalike found), got %d; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "@acme/utils  LOOKALIKE  public registry has this name") {
		t.Errorf("stdout missing lookalike line:\n%s", out)
	}
	if !strings.Contains(out, "@acme/core  ok") {
		t.Errorf("stdout missing ok line:\n%s", out)
	}
	if strings.Contains(out, "lodash") {
		t.Errorf("lodash is not in routed scope and should not be checked:\n%s", out)
	}
	if !strings.Contains(out, "1 lookalike(s) found across 2 private packages") {
		t.Errorf("summary line missing or incorrect:\n%s", out)
	}
}

func TestCheckCommandWithLockfile(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	setupTestNpmrc(t, "acme")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/@acme%2ftransitive" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	pkgJSON := `{
		"name": "my-app",
		"dependencies": {
			"@acme/direct": "^1.0.0"
		}
	}`
	lockJSON := `{
		"name": "my-app",
		"lockfileVersion": 3,
		"packages": {
			"": { "name": "my-app" },
			"node_modules/@acme/direct": { "version": "1.0.0" },
			"node_modules/@acme/transitive": { "version": "1.0.0" }
		}
	}`
	dir := setupTempProject(t, pkgJSON, lockJSON)

	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d; stderr=%s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "@acme/transitive  LOOKALIKE  public registry has this name") {
		t.Errorf("stdout missing lockfile lookalike line:\n%s", out)
	}
	if !strings.Contains(out, "@acme/direct  ok") {
		t.Errorf("stdout missing direct ok line:\n%s", out)
	}
}

func TestCheckCommandJSON(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	setupTestNpmrc(t, "acme")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/@acme%2ffoo" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	pkgJSON := `{
		"dependencies": {
			"@acme/foo": "1.0.0",
			"@acme/bar": "1.0.0"
		}
	}`
	dir := setupTempProject(t, pkgJSON, "")

	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{"--json", dir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d; stderr=%s", code, stderr.String())
	}

	var sum checkSummaryJSON
	if err := json.Unmarshal(stdout.Bytes(), &sum); err != nil {
		t.Fatalf("invalid json output: %v; stdout=%s", err, stdout.String())
	}
	if sum.Total != 2 || sum.Lookalikes != 1 {
		t.Fatalf("unexpected summary: total=%d, lookalikes=%d", sum.Total, sum.Lookalikes)
	}
	if len(sum.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(sum.Packages))
	}
	if sum.Packages[0].Name != "@acme/bar" || sum.Packages[0].Status != "ok" {
		t.Errorf("unexpected package 0: %+v", sum.Packages[0])
	}
	if sum.Packages[1].Name != "@acme/foo" || sum.Packages[1].Status != "lookalike" {
		t.Errorf("unexpected package 1: %+v", sum.Packages[1])
	}
}

func TestCheckCommandAllOK(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	setupTestNpmrc(t, "acme")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	pkgJSON := `{
		"dependencies": {
			"@acme/clean": "1.0.0"
		}
	}`
	dir := setupTempProject(t, pkgJSON, "")

	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "@acme/clean  ok") {
		t.Errorf("expected clean ok line, got:\n%s", out)
	}
	if !strings.Contains(out, "0 lookalikes found across 1 private packages") {
		t.Errorf("expected 0 lookalikes in summary, got:\n%s", out)
	}
}

func TestCheckCommandUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Unknown flag
	code := runCheckArgs([]string{"--invalid-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2 for unknown flag, got %d", code)
	}

	// Missing directory
	stderr.Reset()
	code = runCheckArgs([]string{"/nonexistent/directory/path/12345"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2 for missing dir, got %d", code)
	}

	// Empty dir missing package.json
	emptyDir := t.TempDir()
	stderr.Reset()
	code = runCheckArgs([]string{emptyDir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2 for missing package.json, got %d", code)
	}
}

func TestCheckCommandQuietProgress(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	setupTestNpmrc(t, "acme")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	pkgJSON := `{
		"dependencies": {
			"@acme/clean": "1.0.0"
		}
	}`
	dir := setupTempProject(t, pkgJSON, "")

	// Set quiet = true (as parseQuiet would)
	oldQuiet := quiet
	quiet = true
	defer func() { quiet = oldQuiet }()

	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}

	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr when quiet, got:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "@acme/clean  ok") {
		t.Errorf("expected stdout output even when quiet, got:\n%s", stdout.String())
	}
}

func TestCheckCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCheckArgs([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0 for --help, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage: consona check") {
		t.Errorf("help text missing: %s", stdout.String())
	}
}

// silence unused import in npmadapter
var _ = npmadapter.UserConfig
