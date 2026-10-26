package creds

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

// Order documents the resolution order for an owner (used in help/docs).
const Order = `For a package from GitHub owner <owner>, consona tries, in order:
  1. CONSONA_TOKEN_<OWNER>   env var (owner uppercased, '-' -> '_')
  2. named tokens added with --for @<owner>   (consona token add)
  3. CONSONA_TOKEN           env var
  4. GH_TOKEN, GITHUB_TOKEN  env vars
  5. the default named token (consona token add --default)
  6. the gh login            (gh auth token / gh config get oauth_token)
If a credential is rejected (401), cannot see the repo (404/403) or is
rate limited, the next one is tried; the error lists every source tried.`

// Candidate is one credential source for an owner.
type Candidate struct {
	Source  string    // human label, e.g. `named token "work"`, "env GH_TOKEN"
	Kind    string    // env | named | gh
	Name    string    // token name (named tokens)
	Expires time.Time // from the index (named tokens), zero if unknown
	Value   string    // the token; never print it (use redact.Mask)
	Err     error     // why this source is unavailable (Value is empty)
}

// Label is the Source plus the masked token, safe to print.
func (c Candidate) Label() string {
	if c.Value == "" {
		return c.Source
	}
	return c.Source + " (" + redact.Mask(c.Value) + ")"
}

// ErrUnset marks a source that is simply not configured.
var ErrUnset = errors.New("not set")

// Resolver lists the credentials to try for an owner. Values are read at
// call time (env, secure store, gh) and cached in memory for TTL only.
type Resolver struct {
	Getenv    func(string) string
	Store     Store
	IndexPath string
	GH        func() (string, error) // the gh login token
	TTL       time.Duration

	mu    sync.Mutex
	cache map[string]cached
	ghAt  time.Time
	ghVal string
	ghErr error
}

type cached struct {
	at    time.Time
	cands []Candidate
}

// NewResolver uses the process environment, the OS secure store, the
// token index in ConfigDir and the gh CLI.
func NewResolver() *Resolver {
	return &Resolver{Getenv: os.Getenv, Store: SystemStore{}, IndexPath: IndexPath(), GH: GHLogin, TTL: time.Minute}
}

// EnvName is the per-owner override variable for owner.
func EnvName(owner string) string {
	return "CONSONA_TOKEN_" + strings.ToUpper(strings.ReplaceAll(NormOwner(owner), "-", "_"))
}

// GHLogin returns the token of the gh login (gh honours GH_CONFIG_DIR):
// `gh auth token` (gh >= 2.17), else `gh config get oauth_token`.
func GHLogin() (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh is not installed (optional)")
	}
	for _, args := range [][]string{{"auth", "token"}, {"config", "get", "-h", "github.com", "oauth_token"}} {
		cmd := exec.Command("gh", args...)
		cmd.Env = append(os.Environ(), "GH_TOKEN=", "GITHUB_TOKEN=") // the env vars are their own sources
		out, err := cmd.Output()
		if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
			return tok, nil
		}
	}
	return "", fmt.Errorf("gh is installed but not logged in (`gh auth login`)")
}

// Invalidate drops cached values (after token add/remove).
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cache = nil
	r.ghAt = time.Time{}
	r.mu.Unlock()
}

// Candidates returns every source for owner in resolution order, with
// unavailable ones carrying Err. Owner "" means "no particular owner"
// (per-owner sources are skipped). Identical values are listed once, at
// their first position.
func (r *Resolver) Candidates(owner string) []Candidate {
	owner = NormOwner(owner)
	r.mu.Lock()
	if c, ok := r.cache[owner]; ok && time.Since(c.at) < r.TTL {
		r.mu.Unlock()
		return c.cands
	}
	r.mu.Unlock()
	cands := r.compute(owner)
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[owner] = cached{time.Now(), cands}
	r.mu.Unlock()
	return cands
}

func (r *Resolver) compute(owner string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(c Candidate) {
		if c.Value != "" {
			if seen[c.Value] {
				return
			}
			seen[c.Value] = true
			redact.AddSecret(c.Value)
		}
		out = append(out, c)
	}
	env := func(k string) {
		if v := strings.TrimSpace(r.Getenv(k)); v != "" {
			add(Candidate{Source: "env " + k, Kind: "env", Value: v})
		} else {
			add(Candidate{Source: "env " + k, Kind: "env", Err: ErrUnset})
		}
	}
	var ix *Index
	var ixErr error
	if r.IndexPath != "" {
		ix, ixErr = LoadIndex(r.IndexPath)
	}
	if ix == nil {
		ix = &Index{}
	}
	named := func(t Token, why string) {
		c := Candidate{Source: fmt.Sprintf("named token %q (%s)", t.Name, why), Kind: "named", Name: t.Name, Expires: t.ExpiresAt()}
		if r.Store == nil {
			c.Err = &NoStoreError{errors.New("no store configured")}
		} else if v, err := r.Store.Get(t.Name); err != nil {
			c.Err = err
		} else {
			c.Value = strings.TrimSpace(v)
		}
		add(c)
	}
	if owner != "" {
		env(EnvName(owner))
		found := false
		for _, t := range ix.Tokens {
			if t.For(owner) {
				named(t, "--for @"+owner)
				found = true
			}
		}
		if !found {
			msg := "none added with --for @" + owner
			if ixErr != nil {
				msg = ixErr.Error()
			}
			add(Candidate{Source: "named tokens for @" + owner, Kind: "named", Err: errors.New(msg)})
		}
	}
	env("CONSONA_TOKEN")
	env("GH_TOKEN")
	env("GITHUB_TOKEN")
	def := false
	for _, t := range ix.Tokens {
		if t.Default {
			named(t, "default")
			def = true
		}
	}
	if !def {
		add(Candidate{Source: "default named token", Kind: "named", Err: errors.New("none (consona token add --default)")})
	}
	c := Candidate{Source: "gh login", Kind: "gh"}
	if v, err := r.gh(); err != nil {
		c.Err = err
	} else {
		c.Value = v
	}
	add(c)
	return out
}

// gh runs the gh lookup at most once per TTL.
func (r *Resolver) gh() (string, error) {
	if r.GH == nil {
		return "", errors.New("disabled")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ghAt.IsZero() || time.Since(r.ghAt) >= r.TTL {
		r.ghVal, r.ghErr = r.GH()
		r.ghAt = time.Now()
	}
	return r.ghVal, r.ghErr
}

// Named returns every named token in the index with its value (or Err),
// for setup's owner discovery and `consona token test`.
func (r *Resolver) Named() ([]Token, []Candidate, error) {
	ix, err := LoadIndex(r.IndexPath)
	if err != nil {
		return nil, nil, err
	}
	var cs []Candidate
	for _, t := range ix.Tokens {
		c := Candidate{Source: fmt.Sprintf("named token %q", t.Name), Kind: "named", Name: t.Name, Expires: t.ExpiresAt()}
		if v, err := r.Store.Get(t.Name); err != nil {
			c.Err = err
		} else {
			c.Value = strings.TrimSpace(v)
			redact.AddSecret(c.Value)
		}
		cs = append(cs, c)
	}
	return ix.Tokens, cs, nil
}

// Usable filters candidates that have a value.
func Usable(cs []Candidate) []Candidate {
	var out []Candidate
	for _, c := range cs {
		if c.Value != "" {
			out = append(out, c)
		}
	}
	return out
}

// TokenType names a token's kind from its prefix.
func TokenType(tok string) string {
	switch {
	case strings.HasPrefix(tok, "github_pat_"):
		return "fine-grained"
	case strings.HasPrefix(tok, "ghp_"):
		return "classic"
	case strings.HasPrefix(tok, "gho_"):
		return "oauth"
	case strings.HasPrefix(tok, "ghu_"):
		return "app-user"
	case strings.HasPrefix(tok, "ghs_"):
		return "app-installation"
	case strings.HasPrefix(tok, "ghr_"):
		return "refresh"
	}
	return "unknown"
}

// TypeDescription is a human description of TokenType.
func TypeDescription(t string) string {
	return map[string]string{
		"fine-grained":     "fine-grained personal access token (github_pat_)",
		"classic":          "classic personal access token (ghp_)",
		"oauth":            "OAuth app token (gho_, e.g. from `gh auth login`)",
		"app-user":         "GitHub App user-to-server token (ghu_)",
		"app-installation": "GitHub App installation token (ghs_)",
		"refresh":          "GitHub App refresh token (ghr_; not usable for API calls)",
	}[t]
}
