package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
)

// Fake tokens (never real): each one makes the fake GitHub answer in a
// specific way.
const (
	tokGood    = "github_pat_11GOOD00000goodgoodgood_goodgoodgoodgoodgoodgoodgoodgoodgoodgoodgoodgoodgoodgoodgood0001"
	tokBad     = "ghp_badbadbadbadbadbadbadbadbadbadba0002"
	tokExpired = "github_pat_11EXP000000expexpexpexp_expexpexpexpexpexpexpexpexpexpexpexpexpexpexpexpexpexpexp0003"
	tokNoRepo  = "ghp_norepnorepnorepnorepnorepnorepno0004"
	tokFG404   = "github_pat_11FG40400000fgfgfgfgfgfg_fgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfgfg0005"
	tokSSO     = "gho_ssossossossossossossossossossosso0006"
	tokRate    = "gho_rateratarateratarateratarateratar0007"
	tokBoom    = "gho_boomboomboomboomboomboomboomboom0008"
)

var allToks = []string{tokGood, tokBad, tokExpired, tokNoRepo, tokFG404, tokSSO, tokRate, tokBoom}

type fakeGH struct {
	mu    sync.Mutex
	calls map[string]int // token suffix -> requests
	reset time.Time
}

func (f *fakeGH) count(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[suffix]
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimPrefix(tok, "token ")
	f.mu.Lock()
	f.calls[tok[len(tok)-4:]]++
	f.mu.Unlock()
	h := w.Header()
	msg := func(code int, m string) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"message":%q}`, m)
	}
	switch tok {
	case tokGood:
		h.Set("GitHub-Authentication-Token-Expiration", "2027-01-31 10:00:00 UTC")
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", "4990")
		h.Set("X-RateLimit-Reset", fmt.Sprint(f.reset.Unix()))
		switch {
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"login":"fixture-user","name":"Fixture User","id":42}`)
		case r.URL.Path == "/user/orgs":
			fmt.Fprint(w, `[{"login":"fixture-org"}]`)
		case r.URL.Path == "/user/repos":
			fmt.Fprint(w, `[{"full_name":"fixture-user/lib-a"},{"full_name":"fixture-user/lib-b"},{"full_name":"other/x"}]`)
		case r.URL.Path == "/repos/fixture-user/lib-a/tags":
			fmt.Fprint(w, `[{"name":"v1.0.0","commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`)
		case strings.HasPrefix(r.URL.Path, "/repos/fixture-user/lib-a"):
			fmt.Fprint(w, `{}`)
		case strings.HasPrefix(r.URL.Path, "/repos/fixture-user/lib-b/contents"):
			msg(404, "Not Found") // fine-grained token without Contents: Read
		case strings.HasPrefix(r.URL.Path, "/repos/fixture-user/lib-b"):
			fmt.Fprint(w, `[]`)
		default:
			msg(404, "Not Found")
		}
	case tokBad:
		msg(401, "Bad credentials")
	case tokExpired:
		h.Set("GitHub-Authentication-Token-Expiration", "2026-01-01 00:00:00 UTC")
		msg(401, "Bad credentials")
	case tokNoRepo:
		h.Set("X-OAuth-Scopes", "read:org, gist")
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"login":"scopeless","id":7}`)
			return
		}
		msg(404, "Not Found")
	case tokFG404:
		msg(404, "Not Found")
	case tokSSO:
		h.Set("X-GitHub-SSO", "required; url=https://github.com/orgs/fixture-org/sso?authorization_request=abc")
		msg(403, "Resource protected by organization SAML enforcement. You must grant your OAuth token access to this organization.")
	case tokRate:
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", "0")
		h.Set("X-RateLimit-Reset", fmt.Sprint(f.reset.Unix()))
		msg(403, "API rate limit exceeded for user ID 1.")
	case tokBoom:
		msg(502, "Server Error")
	default:
		msg(401, "Bad credentials")
	}
}

func newFake(t *testing.T, env map[string]string, gh string, store map[string]string, idx ...creds.Token) (*Source, *fakeGH) {
	t.Helper()
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	f := &fakeGH{calls: map[string]int{}, reset: now.Add(17 * time.Minute)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	r := &creds.Resolver{Getenv: func(k string) string { return env[k] }, TTL: time.Minute}
	r.Environ = func() []string {
		var kv []string
		for k, v := range env {
			kv = append(kv, k+"="+v)
		}
		sort.Strings(kv)
		return kv
	}
	if gh != "" {
		r.GH = func() (string, error) { return gh, nil }
	} else {
		r.GH = func() (string, error) { return "", errors.New("gh is not installed (optional)") }
	}
	if len(idx) > 0 {
		r.IndexPath = t.TempDir() + "/tokens.json"
		ix := &creds.Index{}
		for _, tk := range idx {
			ix.Add(tk, false)
		}
		if err := ix.Save(r.IndexPath); err != nil {
			t.Fatal(err)
		}
		r.Store = &creds.MapStore{M: store}
	}
	return &Source{API: srv.URL, Creds: r, Client: srv.Client(), Now: func() time.Time { return now }}, f
}

func noTokens(t *testing.T, s string) {
	t.Helper()
	for _, tok := range allToks {
		if strings.Contains(s, tok) || strings.Contains(s, tok[4:24]) {
			t.Fatalf("token value leaked:\n%s", s)
		}
	}
}

// A rejected credential falls through to the next; the working one is
// then preferred, so the bad one is not retried on every request.
func TestFallthroughAndPreference(t *testing.T) {
	s, f := newFake(t, map[string]string{"CONSONA_TOKEN_FIXTURE_USER": tokBad, "CONSONA_TOKEN": tokFG404}, tokGood, nil)
	ref := core.Ref{Owner: "fixture-user", Name: "lib-a"}
	revs, err := s.Revisions(context.Background(), ref)
	if err != nil || len(revs) == 0 || revs[0].Version != "1.0.0" {
		t.Fatalf("%v %+v", err, revs)
	}
	bad := f.count("0002")
	for i := 0; i < 3; i++ {
		if _, err := s.Revisions(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("0002") != bad {
		t.Fatalf("bad token retried after a working one was found (%d -> %d)", bad, f.count("0002"))
	}
}

// Every source fails, each in its own way: the error lists all of them,
// in order, with the specific cause and fix, and never a token value.
func TestAccessErrorListsEverySource(t *testing.T) {
	s, _ := newFake(t, map[string]string{
		"CONSONA_TOKEN_FIXTURE_ORG": tokBad,
		"CONSONA_TOKEN":             tokExpired,
		"GH_TOKEN":                  tokFG404,
		"GITHUB_TOKEN":              tokNoRepo,
	}, tokRate, map[string]string{"org": tokSSO}, creds.Token{Name: "org", Owners: []string{"fixture-org"}})
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "fixture-org", Name: "secret"})
	var ae *AccessError
	if !errors.As(err, &ae) {
		t.Fatalf("%T %v", err, err)
	}
	msg := err.Error()
	t.Log("\n" + msg)
	noTokens(t, msg)
	order := []string{
		"env CONSONA_TOKEN_FIXTURE_ORG (ghp_…0002): 401 bad credentials",
		`named token "org" (--for @fixture-org) (gho_…0006): 403: the organization uses SAML single sign-on`,
		"https://github.com/orgs/fixture-org/sso?authorization_request=abc",
		"env CONSONA_TOKEN (github_pat_…0003): token expired on",
		"env GH_TOKEN (github_pat_…0005): 404: fixture-org/secret does not exist or this token cannot see it",
		"Repository access must include fixture-org/secret",
		"approve fine-grained tokens",
		"env GITHUB_TOKEN (ghp_…0004): 404: classic token without the `repo` scope",
		"add the `repo` scope at https://github.com/settings/tokens",
		"gh login (gho_…0007): 403: GitHub API rate limit exhausted (limit 5000), resets at",
		"(in 17m0s)",
		"Not available:",
		"default named token: none",
	}
	pos := 0
	for _, want := range order {
		i := strings.Index(msg[pos:], want)
		if i < 0 {
			t.Fatalf("missing (or out of order) %q in\n%s", want, msg)
		}
		pos += i
	}
	for _, want := range []string{"update CONSONA_TOKEN_FIXTURE_ORG in your environment", "https://github.com/settings/personal-access-tokens"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !errors.Is(err, core.ErrAccess) || errors.Is(err, core.ErrNotFound) {
		t.Fatal("mixed failures are an access error, not a plain not-found")
	}
	if ae.Code() != KindRateLimited {
		t.Fatalf("code %s", ae.Code())
	}
}

// Only 404s: still lists the sources, and counts as not found.
func TestAllNotFound(t *testing.T) {
	s, _ := newFake(t, map[string]string{"GH_TOKEN": tokFG404}, tokGood, nil)
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "fixture-user", Name: "nope"})
	if !errors.Is(err, core.ErrNotFound) || !errors.Is(err, core.ErrAccess) {
		t.Fatalf("%v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "env GH_TOKEN") || !strings.Contains(msg, "gh login") {
		t.Fatalf("%s", msg)
	}
}

// A named token's fix says how to replace it.
func TestNamedTokenFix(t *testing.T) {
	s, _ := newFake(t, nil, "", map[string]string{"work": tokBad}, creds.Token{Name: "work", Owners: []string{"fixture-user"}})
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "fixture-user", Name: "lib-a"})
	if err == nil || !strings.Contains(err.Error(), "`consona token add work --replace`") || !strings.Contains(err.Error(), "gh login: gh is not installed") {
		t.Fatalf("%v", err)
	}
}

// Expiry known only from the index (GitHub sent no header).
func TestExpiredFromIndex(t *testing.T) {
	s, _ := newFake(t, nil, "", map[string]string{"old": tokBad}, creds.Token{Name: "old", Owners: []string{"x"}, Expires: "2026-02-01T00:00:00Z"})
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "x", Name: "y"})
	if err == nil || !strings.Contains(err.Error(), "token expired on 2026-02-01") {
		t.Fatalf("%v", err)
	}
}

// A GitHub outage stops instead of burning through every credential.
func TestServerErrorStops(t *testing.T) {
	s, f := newFake(t, map[string]string{"CONSONA_TOKEN": tokBoom}, tokGood, nil)
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "fixture-user", Name: "lib-a"})
	if err == nil || errors.Is(err, core.ErrAccess) || !strings.Contains(err.Error(), "502") {
		t.Fatalf("%v", err)
	}
	if f.count("0001") != 0 {
		t.Fatal("tried the next credential after a 5xx")
	}
}

// No credential at all: say so and how to add one.
func TestNoCredential(t *testing.T) {
	s, _ := newFake(t, nil, "", nil)
	_, err := s.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "lib"})
	msg := fmt.Sprint(err)
	for _, want := range []string{"no GitHub credential available", "consona token add <name> --for @acme", "CONSONA_TOKEN_ACME", "gh auth login", "env CONSONA_TOKEN_ACME, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN not set"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in\n%s", want, msg)
		}
	}
}

func TestInspectAndProbes(t *testing.T) {
	s, _ := newFake(t, nil, "", nil)
	c := creds.Candidate{Source: "test", Value: tokGood}
	m, a, err := s.Inspect(context.Background(), c)
	if err != nil || a != nil {
		t.Fatal(err, a)
	}
	if m.Login != "fixture-user" || m.ID != 42 || m.Name != "Fixture User" || m.Type != "fine-grained" || m.ScopesKnown ||
		!m.Expires.Equal(time.Date(2027, 1, 31, 10, 0, 0, 0, time.UTC)) || m.RateRemaining != 4990 {
		t.Fatalf("%+v", m)
	}
	ps := s.Probes(context.Background(), c, []string{"@fixture-user", "nobody"})
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "%v %s: %s | %s\n", p.OK, p.Name, p.Detail, p.Fix)
	}
	out := b.String()
	t.Log("\n" + out)
	for _, want := range []string{
		"true list accessible repos (GET /user/repos): 3 repos visible",
		"true @fixture-user: repos visible: 2 repos (e.g. fixture-user/lib-a)",
		"true @fixture-user: metadata (fixture-user/lib-a)",
		"true @fixture-user: contents read (fixture-user/lib-a)",
		"true @fixture-user: tags (fixture-user/lib-a)",
		"false @nobody: repos visible: no repos of @nobody are visible to this token | at https://github.com/settings/personal-access-tokens the token's Resource owner must be @nobody",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}

	// Classic token without repo: scopes reported, and its 404s say why.
	m, _, _ = s.Inspect(context.Background(), creds.Candidate{Value: tokNoRepo})
	if !m.ScopesKnown || strings.Join(m.Scopes, ",") != "read:org,gist" || m.Type != "classic" {
		t.Fatalf("%+v", m)
	}
	// 401 on /user comes back as an Attempt.
	_, a, _ = s.Inspect(context.Background(), creds.Candidate{Value: tokBad, Kind: "env", Source: "env CONSONA_TOKEN"})
	if a == nil || a.Kind != KindUnauthorized || !strings.Contains(a.Fix, "update CONSONA_TOKEN") {
		t.Fatalf("%+v", a)
	}
}

func TestOwnersAcrossCredentials(t *testing.T) {
	s, _ := newFake(t, map[string]string{"CONSONA_TOKEN": tokBad}, tokGood, map[string]string{"x": tokFG404}, creds.Token{Name: "x", Owners: []string{"mapped-owner"}})
	owners, err := s.Owners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(owners, ","); got != "mapped-owner,fixture-user,fixture-org" {
		t.Fatalf("%s", got)
	}
	s, _ = newFake(t, map[string]string{"CONSONA_TOKEN": tokBad}, "", nil)
	if _, err := s.Owners(context.Background()); err == nil || !strings.Contains(err.Error(), "env CONSONA_TOKEN (ghp_…0002): 401") {
		t.Fatalf("%v", err)
	}
}

// Setup with only a per-owner variable (no gh, no general token) finds
// that owner from the variable name and the token's own account.
func TestOwnersFromPerOwnerEnv(t *testing.T) {
	s, _ := newFake(t, map[string]string{"CONSONA_TOKEN_SOME_ORG": tokGood}, "", nil)
	owners, err := s.Owners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(owners, ","); got != "some-org,fixture-user,fixture-org" {
		t.Fatalf("%s", got)
	}
}

func TestParseExpiry(t *testing.T) {
	for _, v := range []string{"2026-11-30 12:00:00 UTC", "2026-11-30 17:30:00 +0530", "2026-11-30T12:00:00Z"} {
		got, ok := ParseExpiry(v)
		if !ok || !got.Equal(time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("%s: %v", v, got)
		}
	}
}
