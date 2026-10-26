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

const (
	tokA = "github_pat_11AAAAAAA0aaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1111"
	tokB = "ghp_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2222"
	tokC = "gho_cccccccccccccccccccccccccccccccc3333"
	tokD = "ghp_dddddddddddddddddddddddddddddddd4444"
	tokE = "ghp_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee5555"
	tokF = "github_pat_11FFFFFFF0ffffffffffff_ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff6666"
	tokG = "gho_gggggggggggggggggggggggggggggggg7777"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeIndex(t *testing.T, toks ...Token) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "consona", "tokens.json")
	ix := &Index{}
	for _, tk := range toks {
		if err := ix.Add(tk, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.Save(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func sources(cs []Candidate) []string {
	var out []string
	for _, c := range Usable(cs) {
		out = append(out, c.Source)
	}
	return out
}

// The full documented order, every source present.
func TestResolutionOrder(t *testing.T) {
	p := writeIndex(t,
		Token{Name: "acme", Owners: []string{"acme-co"}},
		Token{Name: "fallback", Default: true},
	)
	store := &MapStore{M: map[string]string{"acme": tokB, "fallback": tokF}}
	r := &Resolver{
		Getenv: env(map[string]string{"CONSONA_TOKEN_ACME_CO": tokA, "CONSONA_TOKEN": tokC, "GH_TOKEN": tokD, "GITHUB_TOKEN": tokE}),
		Store:  store, IndexPath: p, GH: func() (string, error) { return tokG, nil }, TTL: time.Minute,
	}
	got := strings.Join(sources(r.Candidates("@Acme-Co")), " | ")
	want := `env CONSONA_TOKEN_ACME_CO | named token "acme" (--for @acme-co) | env CONSONA_TOKEN | env GH_TOKEN | env GITHUB_TOKEN | named token "fallback" (default) | gh login`
	if got != want {
		t.Fatalf("order:\n got %s\nwant %s", got, want)
	}
	// Another owner: no per-owner sources, the named token for acme is not used.
	got = strings.Join(sources(r.Candidates("other")), " | ")
	want = `env CONSONA_TOKEN | env GH_TOKEN | env GITHUB_TOKEN | named token "fallback" (default) | gh login`
	if got != want {
		t.Fatalf("other owner:\n got %s\nwant %s", got, want)
	}
}

// Nothing but gh; nothing at all; gh missing is not an error by itself.
func TestResolutionSparse(t *testing.T) {
	r := &Resolver{Getenv: env(nil), GH: func() (string, error) { return tokG, nil }, TTL: time.Minute}
	if got := sources(r.Candidates("x")); len(got) != 1 || got[0] != "gh login" {
		t.Fatalf("%v", got)
	}
	r = &Resolver{Getenv: env(map[string]string{"CONSONA_TOKEN": tokC}), GH: func() (string, error) { return "", errors.New("gh is not installed (optional)") }, TTL: time.Minute}
	cs := r.Candidates("x")
	if got := sources(cs); len(got) != 1 || got[0] != "env CONSONA_TOKEN" {
		t.Fatalf("%v", got)
	}
	last := cs[len(cs)-1]
	if last.Kind != "gh" || last.Err == nil || !strings.Contains(last.Err.Error(), "not installed") {
		t.Fatalf("gh candidate %+v", last)
	}
	// No index file: the keyring is never touched.
	r = &Resolver{Getenv: env(nil), Store: &MapStore{Err: errors.New("keyring must not be called")}, IndexPath: filepath.Join(t.TempDir(), "none.json"), TTL: time.Minute}
	for _, c := range r.Candidates("x") {
		if c.Err != nil && strings.Contains(c.Err.Error(), "must not") {
			t.Fatal("keyring touched without named tokens")
		}
	}
}

// The same token in two places is tried once, at its first position.
func TestResolutionDedupe(t *testing.T) {
	r := &Resolver{Getenv: env(map[string]string{"CONSONA_TOKEN": tokC, "GH_TOKEN": tokC}), GH: func() (string, error) { return tokC, nil }, TTL: time.Minute}
	if got := sources(r.Candidates("")); len(got) != 1 || got[0] != "env CONSONA_TOKEN" {
		t.Fatalf("%v", got)
	}
}

// gh is run at most once per TTL, and Invalidate forces a re-read.
func TestResolutionCachesGH(t *testing.T) {
	n := 0
	r := &Resolver{Getenv: env(nil), GH: func() (string, error) { n++; return tokG, nil }, TTL: time.Hour}
	r.Candidates("a")
	r.Candidates("b")
	r.Candidates("a")
	if n != 1 {
		t.Fatalf("gh ran %d times", n)
	}
	r.Invalidate()
	r.Candidates("a")
	if n != 2 {
		t.Fatalf("gh ran %d times after Invalidate", n)
	}
}

func TestEnvName(t *testing.T) {
	for in, want := range map[string]string{"@rakesh-jain-cb1998": "CONSONA_TOKEN_RAKESH_JAIN_CB1998", "Acme": "CONSONA_TOKEN_ACME", "@@x-y": "CONSONA_TOKEN_X_Y"} {
		if got := EnvName(in); got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
}

// A secure store error shows up on the named candidate, and the message
// points at the env vars without a plaintext fallback.
func TestNoStore(t *testing.T) {
	p := writeIndex(t, Token{Name: "work", Owners: []string{"acme"}})
	r := &Resolver{Getenv: env(nil), Store: &MapStore{Err: &NoStoreError{errors.New("dbus: no Secret Service")}}, IndexPath: p, TTL: time.Minute}
	var named *Candidate
	cs := r.Candidates("acme")
	for i := range cs {
		if cs[i].Name == "work" {
			named = &cs[i]
		}
	}
	if named == nil || named.Err == nil {
		t.Fatalf("%+v", cs)
	}
	msg := named.Err.Error()
	for _, want := range []string{"secure store", "CONSONA_TOKEN", "plaintext"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
	if errors.As(storeErr(errors.New("x")), new(*NoStoreError)) == false {
		t.Fatal("storeErr should wrap unknown errors as NoStoreError")
	}
}

func TestIndexUniqueAndDefault(t *testing.T) {
	ix := &Index{}
	if err := ix.Add(Token{Name: "work", Default: true}, false); err != nil {
		t.Fatal(err)
	}
	err := ix.Add(Token{Name: "Work"}, false)
	if !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("duplicate: %v", err)
	}
	if err := ix.Add(Token{Name: "work", Description: "new"}, true); err != nil {
		t.Fatal(err)
	}
	if len(ix.Tokens) != 1 || ix.Tokens[0].Description != "new" {
		t.Fatalf("%+v", ix.Tokens)
	}
	ix.Add(Token{Name: "a", Default: true}, false)
	ix.Add(Token{Name: "b", Default: true}, false)
	defs := 0
	for _, tk := range ix.Tokens {
		if tk.Default {
			defs++
		}
	}
	if defs != 1 || !ix.Find("b").Default {
		t.Fatalf("one default expected: %+v", ix.Tokens)
	}
	if !ix.Remove("A") || ix.Find("a") != nil {
		t.Fatal("remove is case-insensitive")
	}
	for _, bad := range []string{"", "has space", "a/b", strings.Repeat("x", 80), "ghp_" + strings.Repeat("a", 36)} {
		if ValidName(bad) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestIndexFileModesAndNoSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg", "consona")
	p := filepath.Join(dir, "tokens.json")
	ix := &Index{}
	ix.Add(Token{Name: "work", Owners: []string{"acme"}, Login: "someone", Type: "fine-grained"}, false)
	if err := ix.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := ix.Save(p); err != nil { // overwrite (atomic rename)
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "github_pat_") || strings.Contains(string(b), "ghp_") {
		t.Fatal("token value in index")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		di, _ := os.Stat(dir)
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left: %v", ents)
	}
	got, err := LoadIndex(p)
	if err != nil || len(got.Tokens) != 1 || got.Tokens[0].Owners[0] != "acme" {
		t.Fatalf("%v %+v", err, got)
	}
	// A missing index is empty, a corrupt one is an error naming the file.
	if ix, err := LoadIndex(filepath.Join(dir, "nope.json")); err != nil || len(ix.Tokens) != 0 {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("{"), 0o600)
	if _, err := LoadIndex(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("corrupt: %v", err)
	}
}

func TestTokenType(t *testing.T) {
	for tok, want := range map[string]string{tokA: "fine-grained", tokB: "classic", tokG: "oauth", "ghs_x": "app-installation", "ghu_x": "app-user", "nope": "unknown"} {
		if got := TokenType(tok); got != want {
			t.Fatalf("%s: %s", tok[:4], got)
		}
	}
}
