// Package redact removes credentials from text before it is printed,
// logged or stored. Token values must never reach a log, a file or test
// output; everything consona writes about a credential goes through here.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// GitHub token formats (classic, fine-grained, OAuth, App user/server,
// refresh) plus the generic "Bearer x"/"token x" header forms.
var patterns = []*regexp.Regexp{
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`(?i)(bearer|basic|token)\s+[A-Za-z0-9_\-\.=:+/]{30,}`),
	regexp.MustCompile(`(?i)(bearer|basic)\s+[A-Za-z0-9_\-\.=:+/]{12,}`),
	regexp.MustCompile(`(?i)(_authToken|_auth|_password|authorization)\s*[=:]\s*\S+`),
}

var (
	mu      sync.RWMutex
	secrets = map[string]bool{}
)

// AddSecret registers a value (e.g. a token read from stdin, the keyring
// or the environment) so that String hides it wherever it appears, even
// if it does not look like a GitHub token. Values shorter than 8 bytes
// are ignored (they would mangle ordinary text).
func AddSecret(v string) {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return
	}
	mu.Lock()
	secrets[v] = true
	mu.Unlock()
}

// String returns s with every known secret and token-looking value
// replaced by [redacted].
func String(s string) string {
	mu.RLock()
	if len(secrets) > 0 {
		vals := make([]string, 0, len(secrets))
		for v := range secrets {
			vals = append(vals, v)
		}
		sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
		for _, v := range vals {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	mu.RUnlock()
	for i, p := range patterns {
		switch i {
		case 2, 3:
			s = p.ReplaceAllString(s, "$1 [redacted]")
		case 4:
			s = p.ReplaceAllString(s, "$1=[redacted]")
		default:
			s = p.ReplaceAllString(s, "[redacted]")
		}
	}
	return s
}

// Mask is the only form in which consona shows a token: its type prefix
// and last 4 characters, e.g. "github_pat_…ab12". Short or unknown tokens
// show only "…" plus the last 4 when the token is long enough.
func Mask(tok string) string {
	tok = strings.TrimSpace(tok)
	prefix := ""
	for _, p := range []string{"github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		if strings.HasPrefix(tok, p) {
			prefix = p
			break
		}
	}
	if len(tok) < 16 {
		return prefix + "…"
	}
	return prefix + "…" + tok[len(tok)-4:]
}
