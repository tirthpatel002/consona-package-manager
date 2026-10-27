package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadLockfile(t *testing.T) {
	tmp := t.TempDir()

	// 1. v3 lockfile
	v3Content := `{
  "name": "project",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "project", "version": "1.0.0" },
    "node_modules/@acme/foo": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48821/npm/@acme/foo/-/foo-1.0.0.tgz"
    },
    "node_modules/public-pkg": {
      "version": "2.0.0",
      "resolved": "https://registry.npmjs.org/public-pkg/-/public-pkg-2.0.0.tgz"
    }
  }
}`
	p3 := filepath.Join(tmp, "v3-lock.json")
	if err := os.WriteFile(p3, []byte(v3Content), 0o600); err != nil {
		t.Fatal(err)
	}

	scopes, port, err := readLockfile(p3)
	if err != nil {
		t.Fatalf("readLockfile(v3) error: %v", err)
	}
	if port != 48821 {
		t.Errorf("port = %d, want 48821", port)
	}
	if len(scopes) != 1 || scopes[0] != "acme" {
		t.Errorf("scopes = %v, want [acme]", scopes)
	}

	// 2. v2 lockfile with nested packages and dependencies
	v2Content := `{
  "name": "project",
  "version": "1.0.0",
  "lockfileVersion": 2,
  "packages": {
    "node_modules/@scope1/core": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48822/npm/@scope1/core/-/core-1.0.0.tgz"
    }
  },
  "dependencies": {
    "@scope2/util": {
      "version": "2.0.0",
      "resolved": "http://127.0.0.1:48822/npm/@scope2/util/-/util-2.0.0.tgz"
    }
  }
}`
	p2 := filepath.Join(tmp, "v2-lock.json")
	if err := os.WriteFile(p2, []byte(v2Content), 0o600); err != nil {
		t.Fatal(err)
	}

	scopes, port, err = readLockfile(p2)
	if err != nil {
		t.Fatalf("readLockfile(v2) error: %v", err)
	}
	if port != 48822 {
		t.Errorf("port = %d, want 48822", port)
	}
	if len(scopes) != 2 || scopes[0] != "scope1" || scopes[1] != "scope2" {
		t.Errorf("scopes = %v, want [scope1, scope2]", scopes)
	}

	// 3. v1 lockfile with nested dependencies
	v1Content := `{
  "name": "project",
  "version": "1.0.0",
  "lockfileVersion": 1,
  "dependencies": {
    "parent": {
      "version": "1.0.0",
      "dependencies": {
        "@nested/child": {
          "version": "0.5.0",
          "resolved": "http://127.0.0.1:48823/npm/@nested/child/-/child-0.5.0.tgz"
        }
      }
    }
  }
}`
	p1 := filepath.Join(tmp, "v1-lock.json")
	if err := os.WriteFile(p1, []byte(v1Content), 0o600); err != nil {
		t.Fatal(err)
	}

	scopes, port, err = readLockfile(p1)
	if err != nil {
		t.Fatalf("readLockfile(v1) error: %v", err)
	}
	if port != 48823 {
		t.Errorf("port = %d, want 48823", port)
	}
	if len(scopes) != 1 || scopes[0] != "nested" {
		t.Errorf("scopes = %v, want [nested]", scopes)
	}

	// 4. Public packages only
	pubContent := `{
  "name": "project",
  "version": "1.0.0",
  "packages": {
    "node_modules/lodash": {
      "version": "4.17.21",
      "resolved": "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz"
    }
  }
}`
	pPub := filepath.Join(tmp, "pub-lock.json")
	if err := os.WriteFile(pPub, []byte(pubContent), 0o600); err != nil {
		t.Fatal(err)
	}
	scopes, port, err = readLockfile(pPub)
	if err != nil {
		t.Fatalf("readLockfile(pub) error: %v", err)
	}
	if port != 0 {
		t.Errorf("port = %d, want 0", port)
	}
	if len(scopes) != 0 {
		t.Errorf("scopes = %v, want empty", scopes)
	}
}

func setupTestProject(t *testing.T, lockfileJSON string) string {
	t.Helper()
	dir := t.TempDir()
	pkgJSON := `{"name": "test-pkg", "version": "1.0.0"}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if lockfileJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lockfileJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// createFakeNpm creates an executable fake npm script in a temp bin dir and prepends it to PATH.
func createFakeNpm(t *testing.T, scriptBody string) string {
	t.Helper()
	binDir := t.TempDir()
	npmPath := filepath.Join(binDir, "npm")
	content := "#!/bin/sh\n" + scriptBody + "\n"
	if err := os.WriteFile(npmPath, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return npmPath
}

func TestCi_NoLockfileError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	dir := setupTestProject(t, "") // no lockfile

	// Capture stderr
	var stderr bytes.Buffer
	vmu.Lock()
	oldVout := vout
	vout = &stderr
	vmu.Unlock()
	defer func() {
		vmu.Lock()
		vout = oldVout
		vmu.Unlock()
	}()

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	// Check stderr for expected error message
	expected := "consona ci needs a package-lock.json; run consona install locally and commit the lockfile"
	if !strings.Contains(stderr.String(), expected) {
		t.Errorf("stderr does not contain %q, got: %s", expected, stderr.String())
	}
}

func TestCi_TempNpmrcContentAndEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CONSONA_HOME", "")
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48821/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	outLog := filepath.Join(t.TempDir(), "npm-run.log")
	npmrcCopy := filepath.Join(t.TempDir(), "captured.npmrc")

	fakeScript := fmt.Sprintf(`
echo "ARGS: $@" >> %s
echo "USERCONFIG: $npm_config_userconfig" >> %s
echo "REGISTRY: $npm_config_registry" >> %s
echo "CONSONA_PORT: $CONSONA_PORT" >> %s
echo "CONSONA_HOME: $CONSONA_HOME" >> %s
if [ -f "$npm_config_userconfig" ]; then
  cp "$npm_config_userconfig" %s
fi
exit 0
`, outLog, outLog, outLog, outLog, outLog, npmrcCopy)

	createFakeNpm(t, fakeScript)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci", "--port", "48821", "--production"})
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0", code)
	}

	// 1. Verify fake npm captured correct env and args
	logBytes, err := os.ReadFile(outLog)
	if err != nil {
		t.Fatalf("reading run log: %v", err)
	}
	logStr := string(logBytes)

	if !strings.Contains(logStr, "ARGS: ci --production --verbose") {
		t.Errorf("args log mismatch: %s", logStr)
	}
	if !strings.Contains(logStr, "REGISTRY: http://127.0.0.1:48821/npm/") {
		t.Errorf("registry env mismatch: %s", logStr)
	}
	if !strings.Contains(logStr, "CONSONA_PORT: 48821") {
		t.Errorf("port env mismatch: %s", logStr)
	}

	// 2. Verify temp .npmrc content
	npmrcBytes, err := os.ReadFile(npmrcCopy)
	if err != nil {
		t.Fatalf("reading captured npmrc: %v", err)
	}
	npmrcStr := string(npmrcBytes)
	if !strings.Contains(npmrcStr, "@acme:registry=http://127.0.0.1:48821/npm/") {
		t.Errorf("temp npmrc missing @acme:registry: %s", npmrcStr)
	}
	if !strings.Contains(npmrcStr, "registry=http://127.0.0.1:48821/npm/") {
		t.Errorf("temp npmrc missing registry line: %s", npmrcStr)
	}
	if !strings.Contains(npmrcStr, "fetch-retry-mintimeout=500") {
		t.Errorf("temp npmrc missing fetch retries: %s", npmrcStr)
	}
	if !strings.Contains(npmrcStr, "loglevel=verbose") {
		t.Errorf("temp npmrc missing loglevel=verbose: %s", npmrcStr)
	}

	// 3. Assert ~/.npmrc untouched (does not exist in home)
	if _, err := os.Stat(filepath.Join(home, ".npmrc")); err == nil {
		t.Errorf("~/.npmrc was created or touched in HOME")
	}
}

func TestCi_CleanupOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48822/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	tempNpmrcPathFile := filepath.Join(t.TempDir(), "npmrc-path.txt")
	tempHomePathFile := filepath.Join(t.TempDir(), "home-path.txt")

	fakeScript := fmt.Sprintf(`
echo "$npm_config_userconfig" > %s
echo "$CONSONA_HOME" > %s
exit 1
`, tempNpmrcPathFile, tempHomePathFile)

	createFakeNpm(t, fakeScript)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci", "--port", "48822"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	// Verify daemon is stopped
	if !isPortFree(48822) {
		t.Errorf("daemon port 48822 is still busy after exit")
	}

	// Verify temp dirs are removed
	npmrcPathBytes, err := os.ReadFile(tempNpmrcPathFile)
	if err == nil {
		npmrcDir := filepath.Dir(strings.TrimSpace(string(npmrcPathBytes)))
		if _, err := os.Stat(npmrcDir); !os.IsNotExist(err) {
			t.Errorf("temp npmrc dir %s was not cleaned up", npmrcDir)
		}
	}

	homePathBytes, err := os.ReadFile(tempHomePathFile)
	if err == nil {
		homeDir := strings.TrimSpace(string(homePathBytes))
		if _, err := os.Stat(homeDir); !os.IsNotExist(err) {
			t.Errorf("temp home dir %s was not cleaned up", homeDir)
		}
	}
}

func TestCi_SignalForwardingAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping signal test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48823/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	sentinel := filepath.Join(t.TempDir(), "npm-started")
	fakeScript := fmt.Sprintf(`
touch %s
trap 'exit 130' INT
trap 'exit 143' TERM
while true; do
  sleep 0.1
done
`, sentinel)

	createFakeNpm(t, fakeScript)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	done := make(chan int, 1)
	go func() {
		done <- run([]string{"ci", "--port", "48823"})
	}()

	// Wait for fake npm to start
	for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("fake npm never started")
	}

	// Send SIGINT to process
	time.Sleep(50 * time.Millisecond)
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	select {
	case code := <-done:
		if code != 130 {
			t.Errorf("signal exit code = %d, want 130", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for consona ci to handle signal and exit")
	}

	// Verify daemon port 48823 was cleaned up
	if !isPortFree(48823) {
		t.Errorf("daemon port 48823 still busy after signal")
	}
}

func TestCi_ExitCodePropagation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48824/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	createFakeNpm(t, `exit 42`)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci", "--port", "48824"})
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestCi_QuietMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48825/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	outLog := filepath.Join(t.TempDir(), "npm-run.log")
	npmrcCopy := filepath.Join(t.TempDir(), "captured.npmrc")

	fakeScript := fmt.Sprintf(`
echo "ARGS: $@" > %s
cp "$npm_config_userconfig" %s
exit 0
`, outLog, npmrcCopy)

	createFakeNpm(t, fakeScript)

	var stderr bytes.Buffer
	vmu.Lock()
	oldVout := vout
	vout = &stderr
	vmu.Unlock()
	defer func() {
		vmu.Lock()
		vout = oldVout
		vmu.Unlock()
	}()

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci", "--port", "48825", "--quiet"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	// 1. Verify stderr was silent (quiet mode)
	if stderr.Len() > 0 {
		t.Errorf("stderr not empty in quiet mode: %s", stderr.String())
	}

	// 2. Verify npm args received --loglevel=notice (not --verbose)
	logBytes, _ := os.ReadFile(outLog)
	logStr := string(logBytes)
	if strings.Contains(logStr, "--verbose") {
		t.Errorf("npm received --verbose in quiet mode: %s", logStr)
	}
	if !strings.Contains(logStr, "--loglevel=notice") {
		t.Errorf("npm missing --loglevel=notice in quiet mode: %s", logStr)
	}

	// 3. Verify temp npmrc does NOT contain loglevel=verbose
	npmrcBytes, _ := os.ReadFile(npmrcCopy)
	if strings.Contains(string(npmrcBytes), "loglevel=verbose") {
		t.Errorf("temp npmrc has loglevel=verbose in quiet mode: %s", string(npmrcBytes))
	}
}

func TestCi_ScopesFlagAndEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())
	t.Setenv("CONSONA_SCOPES", "@envscope1, envscope2")

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@lockscope/pkg": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48826/npm/@lockscope/pkg/-/pkg-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	npmrcCopy := filepath.Join(t.TempDir(), "captured.npmrc")
	fakeScript := fmt.Sprintf(`
cp "$npm_config_userconfig" %s
exit 0
`, npmrcCopy)

	createFakeNpm(t, fakeScript)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci", "--port", "48826", "--scope", "@cliscope1", "--scope=cliscope2"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	npmrcBytes, err := os.ReadFile(npmrcCopy)
	if err != nil {
		t.Fatal(err)
	}
	npmrcStr := string(npmrcBytes)

	for _, s := range []string{"lockscope", "cliscope1", "cliscope2", "envscope1", "envscope2"} {
		expectedLine := fmt.Sprintf("@%s:registry=http://127.0.0.1:48826/npm/", s)
		if !strings.Contains(npmrcStr, expectedLine) {
			t.Errorf("npmrc missing %s in:\n%s", expectedLine, npmrcStr)
		}
	}
}

func TestCi_PortBusyRewrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	// Bind a dummy TCP listener on 48827 to simulate busy lockfile port
	ln, err := net.Listen("tcp", "127.0.0.1:48827")
	if err != nil {
		t.Fatalf("binding dummy listener: %v", err)
	}
	defer ln.Close()

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/lib": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:48827/npm/@acme/lib/-/lib-1.0.0.tgz"
    }
  }
}`
	dir := setupTestProject(t, lockJSON)

	outLog := filepath.Join(t.TempDir(), "npm-run.log")
	npmrcCopy := filepath.Join(t.TempDir(), "captured.npmrc")

	fakeScript := fmt.Sprintf(`
echo "REPLACE_HOST: $npm_config_replace_registry_host" >> %s
echo "PORT: $CONSONA_PORT" >> %s
cp "$npm_config_userconfig" %s
exit 0
`, outLog, outLog, npmrcCopy)

	createFakeNpm(t, fakeScript)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	code := run([]string{"ci"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	logBytes, _ := os.ReadFile(outLog)
	logStr := string(logBytes)

	if !strings.Contains(logStr, "REPLACE_HOST: always") {
		t.Errorf("missing replace_registry_host=always: %s", logStr)
	}

	// Verify the chosen port was not 48827
	lines := strings.Split(logStr, "\n")
	var chosenPort int
	for _, l := range lines {
		if strings.HasPrefix(l, "PORT: ") {
			chosenPort, _ = strconv.Atoi(strings.TrimPrefix(l, "PORT: "))
		}
	}
	if chosenPort == 48827 || chosenPort == 0 {
		t.Errorf("chosenPort = %d, expected a non-busy ephemeral port != 48827", chosenPort)
	}

	npmrcBytes, _ := os.ReadFile(npmrcCopy)
	if !strings.Contains(string(npmrcBytes), "replace-registry-host=always") {
		t.Errorf("npmrc missing replace-registry-host=always: %s", string(npmrcBytes))
	}
}

func TestCi_PortBusyExplicitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping test on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())

	// Bind dummy listener on 48828
	ln, err := net.Listen("tcp", "127.0.0.1:48828")
	if err != nil {
		t.Fatalf("binding dummy listener: %v", err)
	}
	defer ln.Close()

	lockJSON := `{
  "name": "test-pkg",
  "version": "1.0.0",
  "packages": {}
}`
	dir := setupTestProject(t, lockJSON)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	var stderr bytes.Buffer
	vmu.Lock()
	oldVout := vout
	vout = &stderr
	vmu.Unlock()
	defer func() {
		vmu.Lock()
		vout = oldVout
		vmu.Unlock()
	}()

	code := run([]string{"ci", "--port", "48828"})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "port 48828 is already in use") {
		t.Errorf("stderr missing expected error, got: %s", stderr.String())
	}
}
