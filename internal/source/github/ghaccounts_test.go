package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// Fake gh accounts and their fake tokens; each can see some repos.
var (
	ghActive = creds.GHAccount{Login: "pateltirth2", Active: true, Token: "gho_activeactiveactiveactiveacti0101"}
	ghMain   = creds.GHAccount{Login: "tirthpatel002", Token: "gho_mainmainmainmainmainmainmainm0102"}
	ghOther  = creds.GHAccount{Login: "rakesh-jain-cb1998", Token: "gho_otherotherotherotherotherothe0103"}
	visible  = map[string][]string{ // token suffix -> repos it can read
		"0101": {"pateltirth2/stash"},
		"0102": {"tirthpatel002/gradient"},
		"0103": {"acme-org/ui"},
	}
)

type acctGH struct {
	mu      sync.Mutex
	calls   []string // "<suffix> <path>"
	revoked map[string]bool
}

func (f *acctGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	suf := tok[len(tok)-4:]
	f.mu.Lock()
	f.calls = append(f.calls, suf+" "+r.URL.Path)
	revoked := f.revoked[suf]
	f.mu.Unlock()
	if revoked {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
		return
	}
	for _, repo := range visible[suf] {
		p := "/repos/" + repo
		if r.URL.Path == p {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == p+"/tags" {
			fmt.Fprint(w, `[{"name":"v1.2.3","commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]`)
			return
		}
		if strings.HasPrefix(r.URL.Path, p+"/") {
			fmt.Fprint(w, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			return
		}
	}
	w.WriteHeader(404)
	fmt.Fprint(w, `{"message":"Not Found"}`)
}

func (f *acctGH) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func newAcctFake(t *testing.T, mapPath string, accts ...creds.GHAccount) (*Source, *acctGH, *int) {
	t.Helper()
	f := &acctGH{revoked: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	discoveries := 0
	r := &creds.Resolver{Getenv: func(string) string { return "" }, TTL: time.Minute,
		GH:         func() (string, error) { return "", errors.New("legacy gh login must not be used") },
		GHAccounts: func() ([]creds.GHAccount, error) { discoveries++; return accts, nil },
		GHMap:      &creds.GHMap{Path: mapPath}}
	return &Source{API: srv.URL, Creds: r, Client: srv.Client()}, f, &discoveries
}

func captureLog(t *testing.T) (*logstore.Async, logstore.Store) {
	t.Helper()
	t.Setenv("CONSONA_LOG_BACKEND", "jsonl")
	st, err := logstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := logstore.NewAsync(st)
	logstore.SetDefault(a, false)
	t.Cleanup(func() { logstore.SetDefault(nil, false); a.Close() })
	return a, st
}

// resolveMessages waits (up to 2s) for at least n credential.resolve
// messages: Revisions' parallel head request may log after it returns.
func resolveMessages(t *testing.T, a *logstore.Async, st logstore.Store, n int) []string {
	t.Helper()
	var out []string
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		a.Flush(context.Background())
		es, _ := st.Query(logstore.Filter{Event: logstore.EvCredentialResolve})
		out = nil
		for _, e := range es {
			if e.Level != "debug" {
				out = append(out, e.Message)
			}
		}
		if len(out) >= n || time.Now().After(deadline) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond) // and no extra one arrives
	return out
}

// The account whose login is the owner is used first: no request with
// the active account, so no 404 is needed to find it.
func TestGHOwnerMatchNoPrior404(t *testing.T) {
	a, st := captureLog(t)
	s, f, disc := newAcctFake(t, filepath.Join(t.TempDir(), "gh-accounts.json"), ghActive, ghMain, ghOther)
	for i := 0; i < 2; i++ {
		revs, err := s.Revisions(context.Background(), core.Ref{Owner: "tirthpatel002", Name: "gradient"})
		if err != nil || revs[0].Version != "1.2.3" {
			t.Fatalf("%v %+v", err, revs)
		}
	}
	for _, c := range f.take() {
		if !strings.HasPrefix(c, "0102 ") {
			t.Fatalf("request with another account: %s", c)
		}
	}
	if *disc != 1 {
		t.Fatalf("gh accounts discovered %d times (cached for the TTL)", *disc)
	}
	msgs := resolveMessages(t, a, st, 1)
	if len(msgs) != 1 || msgs[0] != "@tirthpatel002/gradient via gh account tirthpatel002 (owner match)" {
		t.Fatalf("%q", msgs)
	}
	noTokensGH(t, strings.Join(msgs, "\n"))
}

// An org: probe once, remember the account (in consona's home), reuse
// it without probing again, also from a new daemon.
func TestGHOrgProbeAndCachedMapping(t *testing.T) {
	a, st := captureLog(t)
	mp := filepath.Join(t.TempDir(), "gh-accounts.json")
	s, f, _ := newAcctFake(t, mp, ghActive, ghMain, ghOther)
	ref := core.Ref{Owner: "acme-org", Name: "ui"}
	if _, err := s.Revisions(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	probes := 0
	for _, c := range f.take() {
		if strings.HasSuffix(c, " /repos/acme-org/ui") {
			probes++
		}
		if strings.HasSuffix(c, "/tags") && !strings.HasPrefix(c, "0103 ") {
			t.Fatalf("tags requested with the wrong account: %s", c)
		}
	}
	if probes != 3 { // active 404, main 404, other 200
		t.Fatalf("probes %d", probes)
	}
	if got := (&creds.GHMap{Path: mp}).Get("acme-org"); got != "rakesh-jain-cb1998" {
		t.Fatalf("mapping %q", got)
	}
	msgs := resolveMessages(t, a, st, 1)
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "@acme-org/ui via gh account rakesh-jain-cb1998 (probed: ") {
		t.Fatalf("%q", msgs)
	}
	// A new daemon (fresh Source and Resolver) reuses the mapping.
	s2, f2, _ := newAcctFake(t, mp, ghActive, ghMain, ghOther)
	if _, err := s2.Revisions(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	for _, c := range f2.take() {
		if !strings.HasPrefix(c, "0103 ") || strings.HasSuffix(c, " /repos/acme-org/ui") {
			t.Fatalf("re-probed or used another account: %s", c)
		}
	}
	if msgs := resolveMessages(t, a, st, 2); len(msgs) != 2 || !strings.Contains(msgs[1], "(remembered for @acme-org)") {
		t.Fatalf("%q", msgs)
	}
}

// A remembered account that stops working falls through to the others,
// and the mapping follows the one that works.
func TestGHRememberedFailsReprobes(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "gh-accounts.json")
	_ = (&creds.GHMap{Path: mp}).Set("acme-org", "tirthpatel002") // stale: main cannot see acme-org/ui
	s, _, _ := newAcctFake(t, mp, ghActive, ghMain, ghOther)
	if _, err := s.Revisions(context.Background(), core.Ref{Owner: "acme-org", Name: "ui"}); err != nil {
		t.Fatal(err)
	}
	if got := (&creds.GHMap{Path: mp}).Get("acme-org"); got != "rakesh-jain-cb1998" {
		t.Fatalf("mapping %q", got)
	}
}

// No account can read the repo: the error names every gh account tried.
func TestGHErrorListsEveryAccount(t *testing.T) {
	s, _, _ := newAcctFake(t, filepath.Join(t.TempDir(), "m.json"), ghActive, ghMain,
		creds.GHAccount{Login: "rakesh-jain-cb1998", Err: errors.New("gh auth token --user rakesh-jain-cb1998: no oauth token")})
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "nobody-org", Name: "secret"})
	var ae *AccessError
	if !errors.As(err, &ae) {
		t.Fatalf("%T %v", err, err)
	}
	msg := err.Error()
	for _, want := range []string{"gh account pateltirth2 (active gh account) (gho_…0101): 404", "gh account tirthpatel002 (other gh account) (gho_…0102): 404", "gh account rakesh-jain-cb1998 (other gh account): gh auth token --user rakesh-jain-cb1998: no oauth token"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in\n%s", want, msg)
		}
	}
	noTokensGH(t, msg)
}

// A rejected (401) gh token makes consona re-read gh's accounts.
func TestGH401RefreshesAccounts(t *testing.T) {
	s, f, disc := newAcctFake(t, filepath.Join(t.TempDir(), "m.json"), ghActive, ghMain)
	f.revoked["0102"] = true
	_, _ = s.Revisions(context.Background(), core.Ref{Owner: "tirthpatel002", Name: "gradient"})
	f.revoked["0102"] = false
	if _, err := s.Revisions(context.Background(), core.Ref{Owner: "tirthpatel002", Name: "gradient"}); err != nil {
		t.Fatal(err)
	}
	if *disc < 2 {
		t.Fatalf("accounts not re-read after a 401 (%d)", *disc)
	}
}

func noTokensGH(t *testing.T, s string) {
	t.Helper()
	for _, a := range []creds.GHAccount{ghActive, ghMain, ghOther} {
		if strings.Contains(s, a.Token) || strings.Contains(s, a.Token[4:20]) {
			t.Fatalf("token leaked:\n%s", s)
		}
	}
}
