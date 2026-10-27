package creds

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Fake gh accounts (never real tokens or a real person's setup).
const multiHosts = `github.com:
    users:
        pateltirth2:
        tirthpatel002:
            oauth_token: gho_insecurestoragenotreadbyconsona0001
        rakesh-jain-cb1998:
    git_protocol: https
    user: pateltirth2
`

func TestParseHostsYML(t *testing.T) {
	logins, active := ParseHostsYML([]byte(multiHosts + "ghe.example.com:\n    users:\n        someone:\n    user: someone\n"))
	if strings.Join(logins, ",") != "pateltirth2,tirthpatel002,rakesh-jain-cb1998" || active != "pateltirth2" {
		t.Fatalf("%v %q", logins, active)
	}
	// gh < 2.40: one account, no users: block.
	logins, active = ParseHostsYML([]byte("github.com:\n    oauth_token: gho_x\n    user: solo\n    git_protocol: https\n"))
	if strings.Join(logins, ",") != "solo" || active != "solo" {
		t.Fatalf("%v %q", logins, active)
	}
}

func TestParseAuthStatus(t *testing.T) {
	out := `github.com
  ✓ Logged in to github.com account pateltirth2 (keyring)
  - Active account: true
  - Git operations protocol: https
  ✓ Logged in to github.com account tirthpatel002 (keyring)
  - Active account: false
`
	logins, active := ParseAuthStatus(out)
	if strings.Join(logins, ",") != "pateltirth2,tirthpatel002" || active != "pateltirth2" {
		t.Fatalf("%v %q", logins, active)
	}
	logins, active = ParseAuthStatus("github.com\n  ✓ Logged in to github.com as solo (/x/hosts.yml)\n")
	if strings.Join(logins, ",") != "solo" || active != "solo" {
		t.Fatalf("%v %q", logins, active)
	}
}

// fakeRunner is gh with --user support (newGH) or without (old gh). It
// records every invocation.
type fakeRunner struct {
	newGH  bool
	tokens map[string]string
	calls  [][]string
}

func (f *fakeRunner) run(env []string, args ...string) (string, string, error) {
	f.calls = append(f.calls, args)
	j := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(j, "auth token") && strings.Contains(j, "--user"):
		if !f.newGH {
			return "", "unknown flag: --user\n\nUsage:  gh auth token [flags]\n", errors.New("exit status 1")
		}
		u := args[len(args)-1]
		if tok, ok := f.tokens[u]; ok {
			return tok + "\n", "", nil
		}
		return "", "no oauth token found for github.com account " + u, errors.New("exit status 1")
	case strings.HasPrefix(j, "auth status"):
		return "", "github.com\n  ✓ Logged in to github.com account pateltirth2 (keyring)\n  - Active account: true\n  ✓ Logged in to github.com account tirthpatel002 (keyring)\n  - Active account: false\n", nil
	}
	return "", "unknown command", errors.New("exit status 1")
}

func ghEnv(dir string) func(string) string {
	return func(k string) string {
		if k == "GH_CONFIG_DIR" {
			return dir
		}
		return ""
	}
}

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDiscoverGHMultipleAccounts(t *testing.T) {
	dir := writeHosts(t, multiHosts)
	f := &fakeRunner{newGH: true, tokens: map[string]string{"pateltirth2": "gho_aaaaaaaaaaaaaaaaaaaaaaaaaaaa0001", "tirthpatel002": "gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbb0002"}}
	accts, err := DiscoverGH(f.run, ghEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 3 || accts[0].Login != "pateltirth2" || !accts[0].Active || accts[1].Token == "" || accts[2].Err == nil {
		t.Fatalf("%+v", accts)
	}
	if strings.Contains(accts[2].Err.Error(), "gho_") {
		t.Fatal("token in error")
	}
	assertReadOnly(t, f.calls, dir, multiHosts)
}

// Keyring-only setups: hosts.yml has no users, `gh auth status` does.
func TestDiscoverGHFromAuthStatus(t *testing.T) {
	dir := writeHosts(t, "github.com:\n    git_protocol: https\n")
	f := &fakeRunner{newGH: true, tokens: map[string]string{"pateltirth2": "gho_aaaaaaaaaaaaaaaaaaaaaaaaaaaa0001", "tirthpatel002": "gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbb0002"}}
	accts, err := DiscoverGH(f.run, ghEnv(dir))
	if err != nil || len(accts) != 2 || accts[0].Login != "pateltirth2" || !accts[0].Active {
		t.Fatalf("%v %+v", err, accts)
	}
}

// gh without --user: the resolver falls back to the single gh login, as
// before this feature.
func TestOldGHDegradesToGHLogin(t *testing.T) {
	dir := writeHosts(t, multiHosts)
	f := &fakeRunner{newGH: false}
	_, err := DiscoverGH(f.run, ghEnv(dir))
	if !errors.Is(err, ErrGHNoUserFlag) {
		t.Fatalf("%v", err)
	}
	r := &Resolver{Getenv: func(string) string { return "" }, TTL: time.Minute,
		GH:         func() (string, error) { return "gho_legacylegacylegacylegacy0009", nil },
		GHAccounts: func() ([]GHAccount, error) { return DiscoverGH(f.run, ghEnv(dir)) }}
	u := Usable(r.Candidates("tirthpatel002"))
	if len(u) != 1 || u[0].Source != "gh login" {
		t.Fatalf("%+v", u)
	}
	assertReadOnly(t, f.calls, dir, multiHosts)
}

func TestCandidatesOrderGHAccounts(t *testing.T) {
	accts := []GHAccount{
		{Login: "pateltirth2", Active: true, Token: "gho_aaaaaaaaaaaaaaaaaaaaaaaaaaaa0001"},
		{Login: "tirthpatel002", Token: "gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbb0002"},
		{Login: "rakesh-jain-cb1998", Token: "gho_cccccccccccccccccccccccccccc0003"},
	}
	m := &GHMap{Path: filepath.Join(t.TempDir(), "gh-accounts.json")}
	r := &Resolver{Getenv: func(k string) string {
		if k == "CONSONA_TOKEN" {
			return "ghp_explicitexplicitexplicitexpl0004"
		}
		return ""
	}, TTL: time.Minute, GHAccounts: func() ([]GHAccount, error) { return accts, nil }, GHMap: m}
	srcs := func(owner string) string {
		var s []string
		for _, c := range Usable(r.Candidates(owner)) {
			s = append(s, c.Source)
		}
		return strings.Join(s, " | ")
	}
	// Explicit credentials keep precedence; then the owner-matching account.
	want := "env CONSONA_TOKEN | gh account tirthpatel002 (owner match) | gh account pateltirth2 (active gh account) | gh account rakesh-jain-cb1998 (other gh account)"
	if got := srcs("TirthPatel002"); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if err := m.Set("acme-org", "rakesh-jain-cb1998"); err != nil {
		t.Fatal(err)
	}
	want = "env CONSONA_TOKEN | gh account rakesh-jain-cb1998 (remembered for @acme-org) | gh account pateltirth2 (active gh account) | gh account tirthpatel002 (other gh account)"
	if got := srcs("acme-org"); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if (&GHMap{Path: m.Path}).Get("ACME-ORG") != "rakesh-jain-cb1998" {
		t.Fatal("mapping not persisted")
	}
}

// A real gh-shaped script on PATH: consona only ever runs `gh auth
// status` and `gh auth token --user`, so gh's active account and config
// are untouched.
func TestExecGHScriptActiveUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	bin, dir := t.TempDir(), writeHosts(t, multiHosts)
	log := filepath.Join(bin, "calls.log")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$*" in
  "auth token --hostname github.com --user pateltirth2") echo gho_aaaaaaaaaaaaaaaaaaaaaaaaaaaa0001 ;;
  "auth token --hostname github.com --user tirthpatel002") echo gho_bbbbbbbbbbbbbbbbbbbbbbbbbbbb0002 ;;
  "auth token --hostname github.com --user rakesh-jain-cb1998") echo gho_cccccccccccccccccccccccccccc0003 ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_CONFIG_DIR", dir)
	accts, err := DiscoverGH(ExecGH, os.Getenv)
	if err != nil || len(accts) != 3 || accts[2].Token == "" {
		t.Fatalf("%v %+v", err, accts)
	}
	b, _ := os.ReadFile(log)
	var calls [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		calls = append(calls, strings.Fields(l))
	}
	assertReadOnly(t, calls, dir, multiHosts)
}

func assertReadOnly(t *testing.T, calls [][]string, dir, hosts string) {
	t.Helper()
	for _, c := range calls {
		j := strings.Join(c, " ")
		if !strings.HasPrefix(j, "auth token") && !strings.HasPrefix(j, "auth status") {
			t.Fatalf("gh %s: consona must only read gh", j)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "hosts.yml")); string(b) != hosts {
		t.Fatalf("hosts.yml changed:\n%s", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("gh config dir written: %v", ents)
	}
}
