package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/creds"
)

// Meta is what GitHub tells about a token (GET /user and its headers).
type Meta struct {
	Login, Name   string
	ID            int64
	Type          string   // creds.TokenType
	Scopes        []string // classic/oauth tokens (x-oauth-scopes)
	ScopesKnown   bool
	Expires       time.Time // zero: no expiry reported
	RateLimit     int
	RateRemaining int
	RateReset     time.Time
}

// Inspect reads the token's account and metadata. A failure is returned
// as an Attempt (with a fix).
func (s *Source) Inspect(ctx context.Context, c creds.Candidate) (*Meta, *Attempt, error) {
	resp, err := s.do(ctx, "/user", "application/vnd.github+json", c.Value)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		a := classify(resp, c, "", "", s.now())
		return nil, &a, nil
	}
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, nil, err
	}
	m := &Meta{Login: u.Login, Name: u.Name, ID: u.ID, Type: creds.TokenType(c.Value)}
	if v, ok := resp.Header["X-Oauth-Scopes"]; ok {
		m.ScopesKnown = true
		for _, sc := range strings.Split(strings.Join(v, ","), ",") {
			if sc = strings.TrimSpace(sc); sc != "" {
				m.Scopes = append(m.Scopes, sc)
			}
		}
	}
	if t, ok := ParseExpiry(resp.Header.Get("GitHub-Authentication-Token-Expiration")); ok {
		m.Expires = t
	}
	m.RateLimit, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	m.RateRemaining, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		m.RateReset = time.Unix(r, 0)
	}
	return m, nil, nil
}

// Probe is one capability check.
type Probe struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

// Probes checks what the token can actually do: list accessible repos,
// and for each owner: see its repos, read metadata, read contents and
// list tags of one repo (the first repo of that owner).
func (s *Source) Probes(ctx context.Context, c creds.Candidate, owners []string) []Probe {
	var out []Probe
	typ := creds.TokenType(c.Value)
	var repos []string // owner/name
	var listErr *Attempt
	for page := 1; page <= 10; page++ {
		resp, err := s.do(ctx, fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=full_name", page), "application/vnd.github+json", c.Value)
		if err != nil {
			out = append(out, Probe{Name: "list accessible repos", Detail: err.Error(), Fix: "check your network"})
			return out
		}
		if resp.StatusCode/100 != 2 {
			a := classify(resp, c, "", "", s.now())
			resp.Body.Close()
			listErr = &a
			break
		}
		var batch []struct {
			FullName string `json:"full_name"`
		}
		err = json.NewDecoder(resp.Body).Decode(&batch)
		resp.Body.Close()
		if err != nil {
			break
		}
		for _, r := range batch {
			repos = append(repos, r.FullName)
		}
		if len(batch) < 100 {
			break
		}
	}
	if listErr != nil {
		out = append(out, Probe{Name: "list accessible repos (GET /user/repos)", Detail: listErr.Detail, Fix: listErr.Fix})
	} else {
		out = append(out, Probe{Name: "list accessible repos (GET /user/repos)", OK: true, Detail: fmt.Sprintf("%d repos visible", len(repos))})
	}
	for _, o := range owners {
		o = creds.NormOwner(o)
		var mine []string
		for _, r := range repos {
			if strings.EqualFold(strings.SplitN(r, "/", 2)[0], o) {
				mine = append(mine, r)
			}
		}
		sort.Strings(mine)
		if len(mine) == 0 {
			fix := "make sure the token's account can read @" + o + "'s private repos"
			switch typ {
			case "fine-grained":
				fix = "at " + FineGrainedURL + " the token's Resource owner must be @" + o + " and Repository access must include its repos (or All repositories); for an organization an owner may have to approve fine-grained tokens"
			case "classic":
				fix = "the classic token needs the `repo` scope (" + ClassicURL + ") and its account needs access to @" + o + "'s repos; for SSO orgs authorize the token (Configure SSO)"
			}
			out = append(out, Probe{Name: "@" + o + ": repos visible", Detail: "no repos of @" + o + " are visible to this token", Fix: fix})
			continue
		}
		out = append(out, Probe{Name: "@" + o + ": repos visible", OK: true, Detail: fmt.Sprintf("%d repos (e.g. %s)", len(mine), mine[0])})
		full := mine[0]
		owner, name, _ := strings.Cut(full, "/")
		base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
		for _, p := range []struct{ name, path, fixFG string }{
			{"metadata", base, "Metadata: Read-only (automatic for selected repos)"},
			{"contents read", base + "/contents/", "Permissions > Repository > Contents: Read-only"},
			{"tags", base + "/tags?per_page=1", "Permissions > Repository > Contents: Read-only"},
		} {
			pr := Probe{Name: fmt.Sprintf("@%s: %s (%s)", o, p.name, full)}
			resp, err := s.do(ctx, p.path, "application/vnd.github+json", c.Value)
			if err != nil {
				pr.Detail, pr.Fix = err.Error(), "check your network"
			} else if resp.StatusCode/100 == 2 {
				pr.OK, pr.Detail = true, resp.Status
				resp.Body.Close()
			} else {
				a := classify(resp, c, owner, name, s.now())
				resp.Body.Close()
				pr.Detail, pr.Fix = a.Detail, a.Fix
				if typ == "fine-grained" && (a.Kind == KindNotFound || a.Kind == KindForbidden) {
					pr.Fix = "at " + FineGrainedURL + " give the token " + p.fixFG + " for " + full
				}
			}
			out = append(out, pr)
		}
	}
	return out
}
