package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/config"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

func captureOutput(t *testing.T, fn func() int) (code int, stdoutStr, stderrStr string) {
	t.Helper()
	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	vmu.Lock()
	origVout := vout
	vout = wErr
	vmu.Unlock()

	code = fn()

	_ = wOut.Close()
	_ = wErr.Close()

	var bufOut bytes.Buffer
	var bufErr bytes.Buffer
	_, _ = io.Copy(&bufOut, rOut)
	_, _ = io.Copy(&bufErr, rErr)
	_ = rOut.Close()
	_ = rErr.Close()

	os.Stdout = origStdout
	os.Stderr = origStderr

	vmu.Lock()
	vout = origVout
	vmu.Unlock()

	return code, bufOut.String(), bufErr.String()
}

func setupTestEnv(t *testing.T) (home, projDir string) {
	t.Helper()
	home = t.TempDir()
	projDir = t.TempDir()
	t.Setenv("CONSONA_HOME", home)
	t.Setenv("CONSONA_CONFIG_DIR", home)
	t.Setenv("CONSONA_LOG_DIR", filepath.Join(home, "logs"))
	t.Setenv("CONSONA_QUIET", "0")
	return home, projDir
}

func writeLockfile(t *testing.T, dir string, content string) {
	t.Helper()
	path := filepath.Join(dir, "package-lock.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWhyGitHubSource(t *testing.T) {
	home, projDir := setupTestEnv(t)
	_ = home

	// Populate log store
	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fixedTime := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	err = s.Append(
		logstore.Entry{
			TS:        fixedTime.Add(-time.Minute),
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			Result:    "ok",
			Message:   "github github.com/corp/app",
			Fields: map[string]any{
				"source": "github",
				"where":  "github.com/corp/app",
			},
		},
		logstore.Entry{
			TS:        fixedTime,
			Event:     logstore.EvBuildFinish,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			CommitSHA: "abcdef0123456789abcdef0123456789abcdef01",
			Result:    "ok",
			Fields: map[string]any{
				"tag": "v1.0.0",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
      "integrity": "sha512-corpappintegrity12345"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	if !strings.Contains(errOut, "consona: inspecting provenance for @corp/app") {
		t.Errorf("expected progress message on stderr, got: %q", errOut)
	}

	expectedLines := []string{
		"Package:    @corp/app",
		"Version:    1.0.0",
		"Source:     github",
		"Repo:       corp/app",
		"Commit:     abcdef0123456789abcdef0123456789abcdef01",
		"Tag:        v1.0.0",
		"Built:      2026-10-26T18:00:00Z",
		"Integrity:  sha512-corpappintegrity12345",
		"Resolved:   http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
		"Warnings:   none",
	}
	for _, l := range expectedLines {
		if !strings.Contains(out, l) {
			t.Errorf("missing expected line in output:\nexpected: %q\ngot:\n%s", l, out)
		}
	}
}

func TestWhyLocalSource(t *testing.T) {
	home, projDir := setupTestEnv(t)

	// Populate log store
	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fixedTime := time.Date(2026, 10, 26, 15, 30, 0, 0, time.UTC)
	err = s.Append(
		logstore.Entry{
			TS:        fixedTime,
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@local/pkg",
			Owner:     "local",
			Repo:      "pkg",
			Version:   "1.0.0-local.1.h98765432",
			Result:    "ok",
			Message:   "local /src/packages/local-pkg",
			Fields: map[string]any{
				"source": "local",
				"where":  "/src/packages/local-pkg",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Create build cache on disk
	cacheDir := filepath.Join(home, "cache", "npm", "local", "pkg", "9876543210987654", "1.0.0-local.1.h98765432")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	art := core.Artifact{
		Version: "1.0.0-local.1.h98765432",
		File:    "pkg.tgz",
		SHA512:  "localsig512base64",
	}
	artBs, _ := json.Marshal(art)
	if err := os.WriteFile(filepath.Join(cacheDir, "artifact.json"), artBs, 0o644); err != nil {
		t.Fatal(err)
	}

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@local/pkg": {
      "version": "1.0.0-local.1.h98765432",
      "resolved": "http://127.0.0.1:48731/npm/@local/pkg/-/@local/pkg-1.0.0-local.1.h98765432.tgz",
      "integrity": "sha512-localsig512base64"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@local/pkg", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	expectedLines := []string{
		"Package:    @local/pkg",
		"Version:    1.0.0-local.1.h98765432",
		"Source:     local folder",
		"Repo:       /src/packages/local-pkg",
		"Commit:     9876543210987654",
		"Integrity:  sha512-localsig512base64",
	}
	for _, l := range expectedLines {
		if !strings.Contains(out, l) {
			t.Errorf("missing expected line:\nwant: %q\ngot:\n%s", l, out)
		}
	}
}

func TestWhySSHSource(t *testing.T) {
	_, projDir := setupTestEnv(t)

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fixedTime := time.Date(2026, 10, 26, 16, 45, 0, 0, time.UTC)
	err = s.Append(
		logstore.Entry{
			TS:        fixedTime.Add(-time.Minute),
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@ssh/remote-pkg",
			Owner:     "ssh",
			Repo:      "remote-pkg",
			Version:   "2.1.0",
			Result:    "ok",
			Message:   "ssh server-box (dev@server-box)",
			Fields: map[string]any{
				"source": "ssh",
				"where":  "server-box (dev@server-box)",
			},
		},
		logstore.Entry{
			TS:        fixedTime,
			Event:     logstore.EvBuildFinish,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@ssh/remote-pkg",
			Owner:     "ssh",
			Repo:      "remote-pkg",
			Version:   "2.1.0",
			CommitSHA: "fedcba9876543210fedcba9876543210fedcba98",
			Result:    "ok",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@ssh/remote-pkg": {
      "version": "2.1.0",
      "resolved": "http://127.0.0.1:48731/npm/@ssh/remote-pkg/-/@ssh/remote-pkg-2.1.0.tgz",
      "integrity": "sha512-remotepkgintegrity"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@ssh/remote-pkg", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	expectedLines := []string{
		"Package:    @ssh/remote-pkg",
		"Version:    2.1.0",
		"Source:     ssh remote",
		"Repo:       server-box (dev@server-box)",
		"Commit:     fedcba9876543210fedcba9876543210fedcba98",
	}
	for _, l := range expectedLines {
		if !strings.Contains(out, l) {
			t.Errorf("missing expected line:\nwant: %q\ngot:\n%s", l, out)
		}
	}
}

func TestWhyMissingData(t *testing.T) {
	_, projDir := setupTestEnv(t)

	// No events in log store, no cache on disk
	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@mystery/pkg": {
      "version": "0.5.0"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@mystery/pkg", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	expectedLines := []string{
		"Package:    @mystery/pkg",
		"Version:    0.5.0",
		"Source:     unknown",
		"Repo:       unknown",
		"Commit:     unknown",
		"Tag:        unknown",
		"Built:      unknown",
		"Integrity:  unknown",
		"Resolved:   unknown",
		"Warnings:   none",
	}
	for _, l := range expectedLines {
		if !strings.Contains(out, l) {
			t.Errorf("missing expected line for missing data:\nwant: %q\ngot:\n%s", l, out)
		}
	}
}

func TestWhyNotInLockfile(t *testing.T) {
	_, projDir := setupTestEnv(t)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@absent/pkg", "--dir", projDir})
	})
	if code != 1 {
		t.Fatalf("expected exit code 1 for package not in lockfile, got %d. stdout: %s", code, out)
	}
	if !strings.Contains(errOut, `consona: package "@absent/pkg" is not in package-lock.json`) {
		t.Errorf("expected clear not-in-lockfile error, got: %q", errOut)
	}
}

func TestWhyJSONOutput(t *testing.T) {
	_, projDir := setupTestEnv(t)

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fixedTime := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	err = s.Append(
		logstore.Entry{
			TS:        fixedTime.Add(-time.Minute),
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			Result:    "ok",
			Fields: map[string]any{
				"source": "github",
				"where":  "github.com/corp/app",
			},
		},
		logstore.Entry{
			TS:        fixedTime,
			Event:     logstore.EvBuildFinish,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			CommitSHA: "abcdef0123456789abcdef0123456789abcdef01",
			Result:    "ok",
			Fields: map[string]any{
				"tag": "v1.0.0",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
      "integrity": "sha512-corpappintegrity"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir, "--json"})
	})
	if code != 0 {
		t.Fatalf("run why --json failed with code %d, stderr: %s", code, errOut)
	}

	var prov PackageProvenance
	if err := json.Unmarshal([]byte(out), &prov); err != nil {
		t.Fatalf("failed to parse JSON output: %v\noutput was: %s", err, out)
	}

	if prov.Package != "@corp/app" {
		t.Errorf("Package = %q, want @corp/app", prov.Package)
	}
	if prov.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", prov.Version)
	}
	if prov.Source != "github" {
		t.Errorf("Source = %q, want github", prov.Source)
	}
	if prov.Repo != "corp/app" {
		t.Errorf("Repo = %q, want corp/app", prov.Repo)
	}
	if prov.Commit != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("Commit = %q, want abcdef0123456789abcdef0123456789abcdef01", prov.Commit)
	}
	if prov.Tag != "v1.0.0" {
		t.Errorf("Tag = %q, want v1.0.0", prov.Tag)
	}
	if prov.Built != "2026-10-26T18:00:00Z" {
		t.Errorf("Built = %q, want 2026-10-26T18:00:00Z", prov.Built)
	}
	if prov.Integrity != "sha512-corpappintegrity" {
		t.Errorf("Integrity = %q, want sha512-corpappintegrity", prov.Integrity)
	}
	if prov.Resolved != "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz" {
		t.Errorf("Resolved = %q", prov.Resolved)
	}
	if len(prov.Warnings) != 0 {
		t.Errorf("Warnings = %v, want empty", prov.Warnings)
	}
}

func TestWhyWithWarnings(t *testing.T) {
	_, projDir := setupTestEnv(t)

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Append(
		logstore.Entry{
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@warned/pkg",
			Owner:     "warned",
			Repo:      "pkg",
			Version:   "1.0.0",
			Result:    "ok",
			Fields:    map[string]any{"source": "github", "where": "github.com/warned/pkg"},
		},
		logstore.Entry{
			Event:     logstore.EvSourceNameMismatch,
			Level:     "warn",
			Ecosystem: "npm",
			Package:   "@warned/pkg",
			Owner:     "warned",
			Repo:      "pkg",
			Version:   "1.0.0",
			Result:    "name-mismatch",
			Message:   `package.json in github.com/warned/pkg says its name is "other-pkg"`,
		},
		logstore.Entry{
			Event:     logstore.EvSourceTagMoved,
			Level:     "warn",
			Ecosystem: "npm",
			Package:   "@warned/pkg",
			Owner:     "warned",
			Repo:      "pkg",
			Version:   "1.0.0",
			Result:    "tag-moved",
			Message:   `tag v1.0.0 now points at commit bbbbbbb`,
		},
	)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@warned/pkg": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@warned/pkg/-/@warned/pkg-1.0.0.tgz",
      "integrity": "sha512-warnedintegrity"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@warned/pkg", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	if !strings.Contains(out, `package.json in github.com/warned/pkg says its name is "other-pkg"`) {
		t.Errorf("missing name mismatch warning in output: %s", out)
	}
	if !strings.Contains(out, `tag v1.0.0 now points at commit bbbbbbb`) {
		t.Errorf("missing tag moved warning in output: %s", out)
	}
}

func TestReportMarkdownAndFiltering(t *testing.T) {
	_, projDir := setupTestEnv(t)

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Append(
		logstore.Entry{
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			Result:    "ok",
			Fields:    map[string]any{"source": "github", "where": "github.com/corp/app"},
		},
		logstore.Entry{
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@local/tool",
			Owner:     "local",
			Repo:      "tool",
			Version:   "2.0.0",
			Result:    "ok",
			Fields:    map[string]any{"source": "local", "where": "/src/tool"},
		},
	)

	// Lockfile contains:
	// - @corp/app (private: points to 127.0.0.1 consona daemon)
	// - @local/tool (private: points to 127.0.0.1 consona daemon)
	// - lodash (public: points to registry.npmjs.org)
	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
      "integrity": "sha512-corpapp"
    },
    "node_modules/@local/tool": {
      "version": "2.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@local/tool/-/@local/tool-2.0.0.tgz",
      "integrity": "sha512-localtool"
    },
    "node_modules/lodash": {
      "version": "4.17.21",
      "resolved": "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz",
      "integrity": "sha512-lodashsig"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"report", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run report failed with code %d, stderr: %s", code, errOut)
	}

	if !strings.Contains(out, "# Package Provenance Report") {
		t.Errorf("missing title in report: %s", out)
	}
	if !strings.Contains(out, "| Package | Version | Source | Repo | Commit | Tag | Built | Integrity | Resolved | Warnings |") {
		t.Errorf("missing table header in report: %s", out)
	}
	if !strings.Contains(out, "@corp/app") {
		t.Errorf("expected @corp/app in report: %s", out)
	}
	if !strings.Contains(out, "@local/tool") {
		t.Errorf("expected @local/tool in report: %s", out)
	}
	// lodash must NOT be in the report!
	if strings.Contains(out, "lodash") {
		t.Errorf("public package lodash should NOT be in provenance report: %s", out)
	}
}

func TestReportJSON(t *testing.T) {
	_, projDir := setupTestEnv(t)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
      "integrity": "sha512-corpapp"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"report", "--dir", projDir, "--json"})
	})
	if code != 0 {
		t.Fatalf("run report --json failed with code %d, stderr: %s", code, errOut)
	}

	var provs []PackageProvenance
	if err := json.Unmarshal([]byte(out), &provs); err != nil {
		t.Fatalf("failed to parse JSON array: %v\noutput was: %s", err, out)
	}
	if len(provs) != 1 {
		t.Fatalf("expected 1 package in report, got %d", len(provs))
	}
	if provs[0].Package != "@corp/app" || provs[0].Version != "1.0.0" {
		t.Errorf("unexpected provenance data: %+v", provs[0])
	}
}

func TestReportOutFile(t *testing.T) {
	_, projDir := setupTestEnv(t)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz",
      "integrity": "sha512-corpapp"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	outFile := filepath.Join(t.TempDir(), "report.md")
	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"report", "--dir", projDir, "--out", outFile})
	})
	if code != 0 {
		t.Fatalf("run report --out failed with code %d, stderr: %s", code, errOut)
	}
	_ = out

	fi, err := os.Stat(outFile)
	if err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
	// Check permissions 0644 (masking umask for test)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %o, want 0644", fi.Mode().Perm())
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "# Package Provenance Report") {
		t.Errorf("file missing header: %s", string(content))
	}
	if !strings.Contains(string(content), "@corp/app") {
		t.Errorf("file missing @corp/app: %s", string(content))
	}
}

func TestWhyLockfileV1Fallback(t *testing.T) {
	_, projDir := setupTestEnv(t)

	lockContent := `{
  "name": "my-v1-project",
  "lockfileVersion": 1,
  "dependencies": {
    "@corp/v1pkg": {
      "version": "0.9.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/v1pkg/-/@corp/v1pkg-0.9.0.tgz",
      "integrity": "sha512-v1integrity"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/v1pkg", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why on v1 lockfile failed with code %d, stderr: %s", code, errOut)
	}

	if !strings.Contains(out, "Package:    @corp/v1pkg") {
		t.Errorf("missing Package in output: %s", out)
	}
	if !strings.Contains(out, "Version:    0.9.0") {
		t.Errorf("missing Version in output: %s", out)
	}
	if !strings.Contains(out, "Integrity:  sha512-v1integrity") {
		t.Errorf("missing Integrity in output: %s", out)
	}
}

func TestWhyNodeModulesPackageJsonFallback(t *testing.T) {
	_, projDir := setupTestEnv(t)

	// Lockfile with empty version
	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	// Create node_modules/@corp/app/package.json
	pkgDir := filepath.Join(projDir, "node_modules", "@corp", "app")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version": "1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	if !strings.Contains(out, "Version:    1.2.3") {
		t.Errorf("expected version 1.2.3 from node_modules package.json, got:\n%s", out)
	}
}

func TestWhyQuietMode(t *testing.T) {
	_, projDir := setupTestEnv(t)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir, "--quiet"})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}
	_ = out

	if strings.Contains(errOut, "consona:") {
		t.Errorf("expected no stderr progress under --quiet, got: %q", errOut)
	}
}

func TestWhyNoLogStoreDoesNotCreate(t *testing.T) {
	home, projDir := setupTestEnv(t)

	logDir := config.LogDir()
	// Ensure log directory is completely absent
	_ = os.RemoveAll(logDir)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, _, errOut := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d, stderr: %s", code, errOut)
	}

	// Verify events.db and jsonl do not exist
	if _, err := os.Stat(filepath.Join(logDir, "events.db")); !os.IsNotExist(err) {
		t.Errorf("events.db was unexpectedly created in read-only command!")
	}
	if _, err := os.Stat(filepath.Join(logDir, "jsonl")); !os.IsNotExist(err) {
		t.Errorf("jsonl dir was unexpectedly created in read-only command!")
	}
	_ = home
}

func TestWhyRedaction(t *testing.T) {
	_, projDir := setupTestEnv(t)

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	secretToken := "ghp_SECRETTOKEN1234567890123456789012345"
	_ = s.Append(
		logstore.Entry{
			Event:     logstore.EvSourceNameMismatch,
			Level:     "warn",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			Result:    "warn",
			Message:   "warning with token " + secretToken,
		},
	)

	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, _ := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d", code)
	}

	if strings.Contains(out, secretToken) {
		t.Fatalf("secret token was leaked in output: %s", out)
	}
}

func TestReportEmptyPrivate(t *testing.T) {
	_, projDir := setupTestEnv(t)

	// Only public packages
	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/react": {
      "version": "18.2.0",
      "resolved": "https://registry.npmjs.org/react/-/react-18.2.0.tgz"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, _ := captureOutput(t, func() int {
		return run([]string{"report", "--dir", projDir})
	})
	if code != 0 {
		t.Fatalf("run report failed with code %d", code)
	}
	if !strings.Contains(out, "# Package Provenance Report") {
		t.Errorf("missing title in report: %s", out)
	}

	codeJSON, outJSON, _ := captureOutput(t, func() int {
		return run([]string{"report", "--dir", projDir, "--json"})
	})
	if codeJSON != 0 {
		t.Fatalf("run report --json failed with code %d", codeJSON)
	}
	var arr []any
	if err := json.Unmarshal([]byte(outJSON), &arr); err != nil {
		t.Fatalf("failed to parse empty JSON array: %v", err)
	}
	if len(arr) != 0 {
		t.Errorf("expected 0 packages, got %d", len(arr))
	}
}

func TestWhyVersionMatching(t *testing.T) {
	home, projDir := setupTestEnv(t)
	_ = home

	s, err := logstore.Open(config.LogDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t1 := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	_ = s.Append(
		logstore.Entry{
			TS:        t1,
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			Result:    "ok",
			Fields:    map[string]any{"source": "github", "where": "github.com/corp/app", "tag": "v1.0.0"},
		},
		logstore.Entry{
			TS:        t1.Add(time.Second),
			Event:     logstore.EvBuildFinish,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "1.0.0",
			CommitSHA: "1111111111111111111111111111111111111111",
			Result:    "ok",
			Fields:    map[string]any{"tag": "v1.0.0"},
		},
		logstore.Entry{
			TS:        t2,
			Event:     logstore.EvSourceResolve,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "2.0.0",
			Result:    "ok",
			Fields:    map[string]any{"source": "github", "where": "github.com/corp/app", "tag": "v2.0.0"},
		},
		logstore.Entry{
			TS:        t2.Add(time.Second),
			Event:     logstore.EvBuildFinish,
			Level:     "info",
			Ecosystem: "npm",
			Package:   "@corp/app",
			Owner:     "corp",
			Repo:      "app",
			Version:   "2.0.0",
			CommitSHA: "2222222222222222222222222222222222222222",
			Result:    "ok",
			Fields:    map[string]any{"tag": "v2.0.0"},
		},
	)

	// Lockfile specifies v1.0.0
	lockContent := `{
  "name": "my-project",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@corp/app": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48731/npm/@corp/app/-/@corp/app-1.0.0.tgz"
    }
  }
}`
	writeLockfile(t, projDir, lockContent)

	code, out, _ := captureOutput(t, func() int {
		return run([]string{"why", "@corp/app", "--dir", projDir, "--json"})
	})
	if code != 0 {
		t.Fatalf("run why failed with code %d", code)
	}

	var prov PackageProvenance
	if err := json.Unmarshal([]byte(out), &prov); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if prov.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", prov.Version)
	}
	if prov.Commit != "1111111111111111111111111111111111111111" {
		t.Errorf("Commit = %q, want commit for 1.0.0", prov.Commit)
	}
	if prov.Tag != "v1.0.0" {
		t.Errorf("Tag = %q, want v1.0.0", prov.Tag)
	}
}
