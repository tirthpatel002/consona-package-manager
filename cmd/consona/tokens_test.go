package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
)

const (
	fakeGood = "github_pat_11TESTTEST0testtesttest_testtesttesttesttesttesttesttesttesttesttesttesttesttestab12"
	fakeBad  = "ghp_badbadbadbadbadbadbadbadbadbadbadba9"
)

type harness struct {
	tc       *tokenCmd
	store    *creds.MapStore
	out, err *bytes.Buffer
	env      map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeGood && r.Header.Get("Authorization") != "token "+fakeGood {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("GitHub-Authentication-Token-Expiration", "2027-01-31 10:00:00 UTC")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		switch {
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"login":"fixture-user","name":"Fixture","id":9}`)
		case r.URL.Path == "/user/repos":
			fmt.Fprint(w, `[{"full_name":"acme/lib"}]`)
		case strings.HasSuffix(r.URL.Path, "/contents/"):
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	h := &harness{store: &creds.MapStore{}, out: &bytes.Buffer{}, err: &bytes.Buffer{}, env: map[string]string{}}
	idx := filepath.Join(t.TempDir(), "consona", "tokens.json")
	r := &creds.Resolver{Getenv: func(k string) string { return h.env[k] }, Store: h.store, IndexPath: idx, TTL: 0,
		GH: func() (string, error) { return "", errors.New("gh is not installed (optional)") }}
	h.tc = &tokenCmd{store: h.store, indexPath: idx, src: &github.Source{API: srv.URL, Creds: r, Client: srv.Client()},
		stdout: h.out, stderr: h.err, isTTY: func() bool { return false }, now: func() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }}
	return h
}

func (h *harness) run(stdin string, args ...string) int {
	h.out.Reset()
	h.err.Reset()
	h.tc.stdin = strings.NewReader(stdin)
	return h.tc.run(context.Background(), args)
}

func (h *harness) noLeak(t *testing.T) {
	t.Helper()
	all := h.out.String() + h.err.String()
	b, _ := os.ReadFile(h.tc.indexPath)
	for _, tok := range []string{fakeGood, fakeBad, fakeGood[11:40], fakeBad[4:30]} {
		if strings.Contains(all, tok) || strings.Contains(string(b), tok) {
			t.Fatalf("token leaked:\n%s\n%s", all, b)
		}
	}
}

func TestTokenAddListTestRemove(t *testing.T) {
	h := newHarness(t)
	if rc := h.run(fakeGood+"\n", "add", "work", "--for", "@acme", "--for=@@Other", "--description", "Acme read-only", "--default"); rc != 0 {
		t.Fatalf("rc %d\n%s%s", rc, h.out, h.err)
	}
	out := h.out.String()
	t.Log("\n" + out)
	for _, want := range []string{
		`added token "work" (github_pat_…ab12) to the OS secure store`,
		"type:        fine-grained personal access token (github_pat_)",
		"account:     fixture-user (Fixture), id 9",
		"expires:     2027-01-31",
		"rate limit:  4999/5000",
		"for:         @acme, @other",
		"GitHub's API does not expose a fine-grained token's name, description or permission list",
		"PASS  list accessible repos (GET /user/repos): 1 repos visible",
		"PASS  @acme: repos visible",
		"FAIL  @acme: contents read (acme/lib)",
		"Permissions > Repository > Contents: Read-only",
		"FAIL  @other: repos visible",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if h.store.M["work"] != fakeGood {
		t.Fatal("not in the secure store")
	}
	h.noLeak(t)

	// Unique names.
	if rc := h.run(fakeGood, "add", "WORK"); rc == 0 || !strings.Contains(h.err.String(), "--replace") {
		t.Fatalf("duplicate accepted: %s", h.err)
	}
	// --replace keeps the owner mapping.
	if rc := h.run(fakeGood, "add", "work", "--replace", "--no-verify"); rc != 0 {
		t.Fatal(h.err)
	}
	ix, _ := creds.LoadIndex(h.tc.indexPath)
	if e := ix.Find("work"); e == nil || strings.Join(e.Owners, ",") != "acme,other" || !e.Default || e.Description != "Acme read-only" {
		t.Fatalf("%+v", ix.Tokens)
	}

	if rc := h.run("", "list"); rc != 0 {
		t.Fatal(h.err)
	}
	t.Log("\n" + h.out.String())
	if l := h.out.String(); !strings.Contains(l, "work (default)") || !strings.Contains(l, "@acme,@other") || !strings.Contains(l, "fine-grained") {
		t.Fatalf("%s", l)
	}
	h.noLeak(t)

	h.env["CONSONA_TOKEN"] = fakeBad
	rc := h.run("", "test")
	t.Log("\n" + h.out.String())
	if rc == 0 || !strings.Contains(h.out.String(), `named token "work" (github_pat_…ab12):`) ||
		!strings.Contains(h.out.String(), "env CONSONA_TOKEN (ghp_…dba9):\n  FAIL  GET /user: 401 bad credentials") ||
		!strings.Contains(h.out.String(), "update CONSONA_TOKEN in your environment") ||
		!strings.Contains(h.out.String(), `@acme -> named token "work" (--for @acme)`) {
		t.Fatalf("%s", h.out)
	}
	h.noLeak(t)

	if rc := h.run("", "remove", "Work"); rc != 0 {
		t.Fatal(h.err)
	}
	if _, ok := h.store.M["work"]; ok {
		t.Fatal("still in store")
	}
	if rc := h.run("", "remove", "work"); rc == 0 {
		t.Fatal("removing twice should fail")
	}
}

// Tokens are never accepted as arguments.
func TestTokenRejectsArgv(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"add", "x", "--token=" + fakeGood},
		{"add", "x", "--token", fakeGood},
		{"add", "x", fakeGood},
		{"add", fakeGood},
	} {
		if rc := h.run("", args...); rc == 0 || !strings.Contains(h.err.String(), "refusing a token on the command line") {
			t.Fatalf("%v: rc %d %s", args[:2], rc, h.err)
		}
		h.noLeak(t)
	}
	if _, err := os.Stat(h.tc.indexPath); err == nil {
		t.Fatal("index written")
	}
}

// A rejected token is not stored.
func TestTokenAddBadToken(t *testing.T) {
	h := newHarness(t)
	if rc := h.run(fakeBad, "add", "bad", "--for", "acme"); rc == 0 {
		t.Fatal("accepted")
	}
	if !strings.Contains(h.err.String(), "nothing was stored") || !strings.Contains(h.err.String(), "https://github.com/settings/tokens") || len(h.store.M) != 0 {
		t.Fatalf("%s", h.err)
	}
	h.noLeak(t)
}

// No secure store: clear message, nothing written anywhere.
func TestTokenAddNoSecureStore(t *testing.T) {
	h := newHarness(t)
	h.store.Err = errors.New("The name org.freedesktop.secrets was not provided by any .service files")
	if rc := h.run(fakeGood, "add", "work", "--for", "acme"); rc == 0 {
		t.Fatal("accepted")
	}
	msg := h.err.String()
	for _, want := range []string{"secure store", "CONSONA_TOKEN_<OWNER>", "plaintext"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %s", want, msg)
		}
	}
	if _, err := os.Stat(h.tc.indexPath); err == nil {
		t.Fatal("index written without a stored token")
	}
	h.noLeak(t)
}

func TestNormScope(t *testing.T) {
	if got := strings.Join(dedupe([]string{"@acme", "acme", "@@Acme", "@b"}), ","); got != "acme,b" {
		t.Fatal(got)
	}
}
