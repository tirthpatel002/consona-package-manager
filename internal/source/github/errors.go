package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/redact"
)

// Settings pages linked from fixes.
const (
	FineGrainedURL = "https://github.com/settings/personal-access-tokens"
	ClassicURL     = "https://github.com/settings/tokens"
)

// Failure kinds.
const (
	KindUnauthorized = "unauthorized"       // 401: bad or revoked
	KindExpired      = "expired"            // 401 + expiry in the past
	KindNotFound     = "not-found"          // 404: no such repo, or no access
	KindNoRepoScope  = "missing-repo-scope" // classic token without repo
	KindSSO          = "sso"                // 403 x-github-sso
	KindRateLimited  = "rate-limited"       // 403/429 with no requests left
	KindForbidden    = "forbidden"          // other 403 (permission)
	KindServer       = "server"             // 5xx etc.
	KindUnavailable  = "unavailable"        // source not configured or unreadable
)

// Attempt is one credential's failure for one request.
type Attempt struct {
	Source string // creds.Candidate.Label(): source + masked token
	Status int
	Kind   string
	Detail string
	Fix    string
}

// fallThrough reports whether the next credential should be tried.
func (a Attempt) fallThrough() bool { return a.Kind != KindServer }

func ownerSettingsURL(tokType string) string {
	if tokType == "classic" {
		return ClassicURL
	}
	return FineGrainedURL
}

// classify turns a failed GitHub response into an Attempt. owner/repo
// name what was being read ("" for /user calls).
func classify(resp *http.Response, c creds.Candidate, owner, repo string, now time.Time) Attempt {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var gm struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &gm)
	ghMsg := redact.String(strings.TrimSpace(gm.Message))
	typ := creds.TokenType(c.Value)
	a := Attempt{Source: c.Label(), Status: resp.StatusCode}
	what := "the repo"
	if repo != "" {
		what = owner + "/" + repo
	} else if owner != "" {
		what = "@" + owner
	}
	exp, expKnown := expiry(resp.Header, c)
	switch code := resp.StatusCode; {
	case code == 401 && expKnown && !exp.IsZero() && exp.Before(now):
		a.Kind = KindExpired
		a.Detail = "token expired on " + exp.Local().Format("2006-01-02 15:04 MST")
		a.Fix = "create a new token at " + ownerSettingsURL(typ) + " (or regenerate this one), then " + updateHint(c)
	case code == 401:
		a.Kind = KindUnauthorized
		a.Detail = "401 bad credentials: the token is invalid, revoked or expired"
		if ghMsg != "" {
			a.Detail += " (GitHub: " + ghMsg + ")"
		}
		a.Fix = "create a new token at " + FineGrainedURL + " (fine-grained) or " + ClassicURL + " (classic), then " + updateHint(c)
	case code == 403 && resp.Header.Get("X-GitHub-SSO") != "":
		a.Kind = KindSSO
		a.Detail = "403: the organization uses SAML single sign-on and this token is not authorized for it"
		u := ssoURL(resp.Header.Get("X-GitHub-SSO"))
		if u == "" {
			u = ownerSettingsURL(typ) + " (Configure SSO > Authorize next to the token)"
		}
		a.Fix = "authorize the token for SSO: open " + u
	case (code == 403 || code == 429) && rateLimited(resp.Header):
		a.Kind = KindRateLimited
		reset := rateReset(resp.Header, now)
		a.Detail = fmt.Sprintf("%d: GitHub API rate limit exhausted (limit %s), resets at %s (in %s)", code,
			orUnknown(resp.Header.Get("X-RateLimit-Limit")), reset.Local().Format("15:04:05 MST"), reset.Sub(now).Round(time.Second))
		a.Fix = "wait until the reset, or use another token (each token has its own limit; unauthenticated limits are much lower)"
	case code == 403:
		a.Kind = KindForbidden
		a.Detail = "403 forbidden"
		if ghMsg != "" {
			a.Detail += " (GitHub: " + ghMsg + ")"
		}
		if typ == "fine-grained" {
			a.Fix = "edit the token at " + FineGrainedURL + ": Repository access must include " + what + " and Permissions > Repository > Contents must be Read-only (Metadata: Read-only is added automatically)"
		} else {
			a.Fix = "the account behind this token lacks read access to " + what + "; ask an owner for access, or use a token of an account that has it"
		}
	case code == 404 || code == 451:
		scopes, classic := resp.Header["X-Oauth-Scopes"]
		if typ == "classic" && classic && !hasRepoScope(strings.Join(scopes, ",")) {
			a.Kind = KindNoRepoScope
			a.Detail = "404: classic token without the `repo` scope cannot see private repos (its scopes: " + orNone(strings.Join(scopes, ",")) + ")"
			a.Fix = "add the `repo` scope at " + ClassicURL + " (edit the token), or use a fine-grained token"
			break
		}
		a.Kind = KindNotFound
		a.Detail = "404: " + what + " does not exist or this token cannot see it"
		switch typ {
		case "fine-grained":
			a.Fix = "check the name; then at " + FineGrainedURL + " edit the token: Resource owner must be @" + orUnknown(owner) +
				", Repository access must include " + what + " (or All repositories), Permissions > Contents: Read-only. " +
				"For an organization, an owner may also need to approve fine-grained tokens (org Settings > Personal access tokens) and the token may need SSO authorization"
		case "classic":
			a.Fix = "check the name and that the token's account can read " + what + "; for an org with SAML SSO authorize the token at " + ClassicURL + " (Configure SSO)"
		default:
			a.Fix = "check the name and that the account behind this credential can read " + what + " (ask an owner for access); `gh auth status` shows which account gh uses"
		}
	default:
		a.Kind = KindServer
		a.Detail = fmt.Sprintf("GitHub API answered %s", resp.Status)
		if ghMsg != "" {
			a.Detail += ": " + ghMsg
		}
		a.Fix = "retry later; see https://www.githubstatus.com"
	}
	return a
}

func updateHint(c creds.Candidate) string {
	switch c.Kind {
	case "named":
		return fmt.Sprintf("`consona token add %s --replace` (paste the new token)", c.Name)
	case "env":
		return "update " + strings.TrimPrefix(c.Source, "env ") + " in your environment"
	case "gh":
		return "run `gh auth login` (or `gh auth refresh`)"
	}
	return "update it"
}

// expiry reads the token expiry: GitHub's response header, else the date
// recorded in the token index.
func expiry(h http.Header, c creds.Candidate) (time.Time, bool) {
	if v := h.Get("GitHub-Authentication-Token-Expiration"); v != "" {
		if t, ok := ParseExpiry(v); ok {
			return t, true
		}
	}
	if !c.Expires.IsZero() {
		return c.Expires, true
	}
	return time.Time{}, false
}

// ParseExpiry parses GitHub's github-authentication-token-expiration
// header, e.g. "2026-11-30 12:00:00 UTC" or "2026-11-30 12:00:00 +0530".
func ParseExpiry(v string) (time.Time, bool) {
	for _, f := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(f, strings.TrimSpace(v)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func rateLimited(h http.Header) bool {
	return h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != ""
}

func rateReset(h http.Header, now time.Time) time.Time {
	if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(s, 0)
	}
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return now.Add(time.Duration(s) * time.Second)
	}
	return now.Add(time.Hour)
}

func ssoURL(h string) string {
	for _, part := range strings.Split(h, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "url="); ok {
			return v
		}
	}
	return ""
}

func hasRepoScope(s string) bool {
	for _, sc := range strings.Split(s, ",") {
		if strings.TrimSpace(sc) == "repo" {
			return true
		}
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// AccessError is a request no credential could make. Its message lists
// every source tried (by name, never value) and how to fix each.
type AccessError struct {
	What     string // e.g. "github.com/acme/lib"
	Owner    string
	Attempts []Attempt
	Skipped  []creds.Candidate // sources not available (unset, unreadable)
}

func (e *AccessError) notFoundOnly() bool {
	if len(e.Attempts) == 0 {
		return false
	}
	for _, a := range e.Attempts {
		if a.Kind != KindNotFound && a.Kind != KindNoRepoScope {
			return false
		}
	}
	return true
}

// Is makes AccessError match core.ErrAccess, and core.ErrNotFound when
// every credential simply could not see the repo.
func (e *AccessError) Is(target error) bool {
	return target == core.ErrAccess || (target == core.ErrNotFound && e.notFoundOnly())
}

// Code is a short machine-readable code for logs.
func (e *AccessError) Code() string {
	if len(e.Attempts) == 0 {
		return "no-credential"
	}
	return e.Attempts[len(e.Attempts)-1].Kind
}

func (e *AccessError) Error() string {
	var b strings.Builder
	if len(e.Attempts) == 0 {
		fmt.Fprintf(&b, "cannot read %s: no GitHub credential available.", e.What)
	} else {
		fmt.Fprintf(&b, "cannot read %s with any available GitHub credential; tried:", e.What)
		for i, a := range e.Attempts {
			fmt.Fprintf(&b, "\n  %d. %s: %s.\n     Fix: %s", i+1, a.Source, a.Detail, a.Fix)
		}
	}
	var unset, other []string
	for _, c := range e.Skipped {
		if c.Err == creds.ErrUnset {
			unset = append(unset, strings.TrimPrefix(c.Source, "env "))
		} else {
			other = append(other, c.Source+": "+firstLine(redact.String(c.Err.Error())))
		}
	}
	if len(unset) > 0 || len(other) > 0 {
		b.WriteString("\n  Not available:")
		if len(unset) > 0 {
			b.WriteString(" env " + strings.Join(unset, ", ") + " not set")
		}
		for _, o := range other {
			b.WriteString("\n    - " + o)
		}
	}
	if len(e.Attempts) == 0 {
		owner := e.Owner
		if owner == "" {
			owner = "<owner>"
		}
		fmt.Fprintf(&b, "\n  Fix: `consona token add <name> --for @%s` (token from %s), or set CONSONA_TOKEN / %s / GH_TOKEN, or `gh auth login`.", owner, FineGrainedURL, creds.EnvName(owner))
	}
	return b.String()
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
