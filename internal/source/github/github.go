// Package github is a core.Source backed by the GitHub API. Credentials
// come from creds.Resolver (per-owner env vars, named tokens in the OS
// secure store, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN, the gh login), are
// read at request time and are held in memory only.
package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// Source talks to the GitHub REST API.
type Source struct {
	API    string // e.g. https://api.github.com
	Creds  *creds.Resolver
	Client *http.Client
	Now    func() time.Time // tests

	mu        sync.Mutex
	pref      map[string]string // owner -> source label that last worked
	redirects map[string]string // "owner/repo" (lower) -> the repo's current full name, once seen redirected
	probeMu   sync.Mutex
	announced map[string]time.Time // "owner/repo|source" -> last credential.resolve logged for a gh account
}

// announceEvery is how often the gh account used for a repo is logged
// again (so each install's verbose output says which account it used).
var announceEvery = 30 * time.Second

// announce reports whether the gh account c used for owner/repo should be
// logged again.
func (s *Source) announce(owner, repo, source string) bool {
	if !strings.HasPrefix(source, "gh account ") {
		return false
	}
	k := strings.ToLower(owner+"/"+repo) + "|" + source
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.announced[k]; ok && s.now().Sub(t) < announceEvery && s.now().Sub(t) >= 0 {
		return false
	}
	if s.announced == nil {
		s.announced = map[string]time.Time{}
	}
	s.announced[k] = s.now()
	return true
}

// announceGH logs (daemon log, log store, live verbose output) which gh
// account read owner/repo and why.
func (s *Source) announceGH(owner, repo string, c creds.Candidate, why string, status int) {
	if c.Kind != "gh" || c.Name == "" {
		return
	}
	what := "@" + creds.NormOwner(owner)
	if repo != "" {
		what += "/" + strings.ToLower(repo)
	}
	logstore.Log(logstore.Entry{Event: logstore.EvCredentialResolve, Level: "info", Owner: owner, Repo: repo,
		CredentialSource: c.Source, Result: "ok", HTTPStatus: status,
		Message: fmt.Sprintf("%s via gh account %s (%s)", what, c.Name, why),
		Fields:  map[string]any{"gh_account": c.Name, "why": why}})
}

// needGHProbe: several gh accounts, none matching the owner or
// remembered for it, so which one to use is found by probing once.
func needGHProbe(all []creds.Candidate) bool {
	n := 0
	for _, c := range all {
		if c.Kind != "gh" || c.Name == "" || c.Value == "" {
			continue
		}
		if c.Why == creds.WhyOwnerMatch || c.Why == creds.WhyRemembered {
			return false
		}
		n++
	}
	return n >= 2
}

// probeGH asks GET /repos/owner/repo with each gh account (active one
// first) and remembers the first that can see it. "" if none can.
func (s *Source) probeGH(ctx context.Context, owner, repo string, all []creds.Candidate) string {
	s.probeMu.Lock() // concurrent requests for one owner probe once
	defer s.probeMu.Unlock()
	if again := s.Creds.Candidates(owner); !needGHProbe(again) {
		for _, c := range again {
			if c.Why == creds.WhyRemembered {
				return c.Name
			}
		}
		return ""
	}
	var tried []string
	for _, c := range all {
		if c.Kind != "gh" || c.Name == "" || c.Value == "" {
			continue
		}
		resp, err := s.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), "application/vnd.github+json", c.Value)
		if err != nil {
			return ""
		}
		resp.Body.Close()
		tried = append(tried, fmt.Sprintf("%s: %d", c.Name, resp.StatusCode))
		if resp.StatusCode/100 == 2 {
			_ = s.Creds.GHMap.Set(owner, c.Name)
			s.Creds.InvalidateOwner(owner)
			logstore.Log(logstore.Entry{Event: logstore.EvCredentialResolve, Level: "debug", Owner: owner, Repo: repo,
				CredentialSource: c.Source, Result: "ok", HTTPStatus: resp.StatusCode,
				Message: "probed gh accounts for " + owner + "/" + repo + " (" + strings.Join(tried, ", ") + "): using " + c.Name})
			return c.Name
		}
	}
	logstore.Log(logstore.Entry{Event: logstore.EvCredentialFail, Level: "debug", Owner: owner, Repo: repo, Result: "fail",
		Message: "no gh account can see " + owner + "/" + repo + " (" + strings.Join(tried, ", ") + ")"})
	return ""
}

// New returns a Source using the default credential resolver. It never
// fails: missing credentials are reported per request, naming every
// source that was tried.
func New() *Source {
	return &Source{API: "https://api.github.com", Creds: creds.NewResolver(), Client: &http.Client{Timeout: 60 * time.Second}}
}

func (s *Source) Describe(r core.Ref) string { return "github.com/" + r.String() }

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// do makes one GET with one token.
func (s *Source) do(ctx context.Context, path, accept, tok string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.API+path, nil)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return s.Client.Do(req)
}

// get tries each available credential for owner in resolution order
// (the one that last worked for owner first) and returns the first 2xx
// response. A rejected credential (401/403/404/rate limit) falls through
// to the next; when none works the *AccessError lists every attempt.
func (s *Source) get(ctx context.Context, owner, repo, path, accept string) (*http.Response, error) {
	all := s.Creds.Candidates(owner)
	probed := ""
	if repo != "" && needGHProbe(all) {
		if probed = s.probeGH(ctx, owner, repo, all); probed != "" {
			all = s.Creds.Candidates(owner)
		}
	}
	usable := creds.Usable(all)
	s.mu.Lock()
	pref := s.pref[strings.ToLower(owner)]
	s.mu.Unlock()
	if pref != "" {
		for i, c := range usable {
			if c.Source == pref && i > 0 {
				usable = append([]creds.Candidate{c}, append(append([]creds.Candidate{}, usable[:i]...), usable[i+1:]...)...)
				break
			}
		}
	}
	e := &AccessError{What: "github.com/" + owner, Owner: owner}
	if repo != "" {
		e.What += "/" + repo
	}
	for _, c := range all {
		if c.Value == "" {
			e.Skipped = append(e.Skipped, c)
		}
	}
	for _, c := range usable {
		resp, err := s.do(ctx, path, accept, c.Value)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 == 2 {
			why := c.Why
			if c.Kind == "gh" && c.Name != "" {
				if c.Why != creds.WhyOwnerMatch && owner != "" {
					_ = s.Creds.GHMap.Set(owner, c.Name)
				}
				switch {
				case strings.EqualFold(probed, c.Name):
					why = "probed: first gh account that can see " + owner + "/" + repo + "; remembered for @" + creds.NormOwner(owner)
				case c.Why == creds.WhyRemembered:
					why = "remembered for @" + creds.NormOwner(owner)
				case len(e.Attempts) > 0:
					why += fmt.Sprintf(", after %d failed credential(s)", len(e.Attempts))
				}
			}
			if s.announce(owner, repo, c.Source) {
				s.announceGH(owner, repo, c, why, resp.StatusCode)
			}
			if pref != c.Source {
				s.mu.Lock()
				if s.pref == nil {
					s.pref = map[string]string{}
				}
				s.pref[strings.ToLower(owner)] = c.Source
				s.mu.Unlock()
				if c.Kind != "gh" || c.Name == "" {
					msg := "using " + c.Source
					if len(e.Attempts) > 0 {
						msg += fmt.Sprintf(" after %d failed credential(s)", len(e.Attempts))
					}
					logstore.Log(logstore.Entry{Event: logstore.EvCredentialResolve, Level: "info", Owner: owner, Repo: repo,
						CredentialSource: c.Source, Result: "ok", HTTPStatus: resp.StatusCode, Message: msg})
				}
			}
			if repo != "" {
				s.checkRedirect(ctx, owner, repo, path, resp, c.Value)
			}
			return resp, nil
		}
		a := classify(resp, c, owner, repo, s.now())
		resp.Body.Close()
		e.Attempts = append(e.Attempts, a)
		if c.Kind == "gh" && c.Name != "" {
			if c.Why == creds.WhyRemembered && a.fallThrough() {
				_ = s.Creds.GHMap.Set(owner, "") // re-probed (by this fallthrough) and re-remembered on success
			}
			if resp.StatusCode == http.StatusUnauthorized {
				s.Creds.InvalidateGH()
			}
		}
		logstore.Log(logstore.Entry{Event: logstore.EvCredentialFail, Level: "warn", Owner: owner, Repo: repo,
			CredentialSource: c.Source, Result: "fail", HTTPStatus: a.Status, ErrorCode: a.Kind, Message: a.Detail + ". Fix: " + a.Fix})
		if !a.fallThrough() {
			return nil, fmt.Errorf("GitHub API %s: %s (credential: %s)", path, a.Detail, a.Source)
		}
	}
	return nil, e
}

func repoPath(r core.Ref) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name)
}

// Revisions lists semver tags; with none, falls back to the default branch head.
func (s *Source) Revisions(ctx context.Context, r core.Ref) ([]core.Revision, error) {
	// Ask for the default-branch head in parallel with the tags: it is only
	// needed for untagged repos, but then it costs no extra round trip.
	type headResult struct {
		sha string
		err error
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	headc := make(chan headResult, 1)
	go func() {
		sha, err := s.head(hctx, r)
		headc <- headResult{sha, err}
	}()
	var tags []core.Tag
	for page := 1; page <= 10; page++ {
		resp, err := s.get(ctx, r.Owner, r.Name, fmt.Sprintf("%s/tags?per_page=100&page=%d", repoPath(r), page), "application/vnd.github+json")
		if err != nil {
			return nil, wrap(s, r, err)
		}
		var batch []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		err = json.NewDecoder(resp.Body).Decode(&batch)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, t := range batch {
			tags = append(tags, core.Tag{Name: t.Name, Commit: t.Commit.SHA})
		}
		if len(batch) < 100 {
			break
		}
	}
	if revs := core.RevisionsFromTags(tags, ""); len(revs) > 0 {
		return revs, nil
	}
	h := <-headc
	if h.err != nil {
		return nil, wrap(s, r, h.err)
	}
	return core.RevisionsFromTags(nil, h.sha), nil
}

func (s *Source) head(ctx context.Context, r core.Ref) (string, error) {
	resp, err := s.get(ctx, r.Owner, r.Name, repoPath(r)+"/commits/HEAD", "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 100))
	return strings.TrimSpace(string(b)), err
}

func wrap(s *Source, r core.Ref, err error) error {
	return err
}

// Fetch downloads the commit tarball and extracts it into dir.
func (s *Source) Fetch(ctx context.Context, r core.Ref, commit, dir string) error {
	resp, err := s.get(ctx, r.Owner, r.Name, repoPath(r)+"/tarball/"+url.PathEscape(commit), "application/vnd.github+json")
	if err != nil {
		return wrap(s, r, err)
	}
	defer resp.Body.Close()
	return Extract(resp.Body, dir)
}

// Extract unpacks a GitHub-style tar.gz (single top-level dir) into dir,
// dropping the top-level component and rejecting unsafe paths.
func Extract(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" {
			continue
		}
		rel = filepath.Clean(rel)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("unsafe path in archive: %s", h.Name)
		}
		dst := filepath.Join(dir, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
		// Symlinks and other entry types are skipped (npm pack ignores them too).
	}
}

// Owners returns the owners setup should route as private: for every
// usable credential (general chain and every named token), its login and
// organisations, plus every named token's --for owners. It fails only if
// no credential works at all.
func (s *Source) Owners(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var owners []string
	addOwner := func(o string) {
		o = creds.NormOwner(o)
		if o != "" && !seen[o] {
			seen[o] = true
			owners = append(owners, o)
		}
	}
	envOwners, envCands := s.Creds.OwnerEnv()
	for _, o := range envOwners {
		addOwner(o)
	}
	cands := append(envCands, s.Creds.Candidates("")...)
	if s.Creds.IndexPath != "" {
		if ix, err := creds.LoadIndex(s.Creds.IndexPath); err == nil {
			for _, o := range ix.Owners() {
				addOwner(o)
			}
			if len(ix.Tokens) > 0 {
				_, named, _ := s.Creds.Named()
				cands = append(cands, named...)
			}
		}
	}
	e := &AccessError{What: "your GitHub account (GET /user)"}
	tried := map[string]bool{}
	ok := false
	for _, c := range cands {
		if c.Value == "" {
			e.Skipped = append(e.Skipped, c)
			continue
		}
		if tried[c.Value] {
			continue
		}
		tried[c.Value] = true
		for _, p := range []string{"/user", "/user/orgs?per_page=100"} {
			resp, err := s.do(ctx, p, "application/vnd.github+json", c.Value)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode/100 != 2 {
				e.Attempts = append(e.Attempts, classify(resp, c, "", "", s.now()))
				resp.Body.Close()
				break
			}
			ok = true
			var one struct{ Login string }
			var many []struct{ Login string }
			if p == "/user" {
				err = json.NewDecoder(resp.Body).Decode(&one)
				addOwner(one.Login)
				if one.Login != "" {
					logstore.Log(logstore.Entry{Event: logstore.EvCredentialResolve, Level: "info", Owner: one.Login,
						CredentialSource: c.Source, Result: "ok", Message: "GitHub account @" + one.Login + " via " + c.Source})
				}
			} else {
				err = json.NewDecoder(resp.Body).Decode(&many)
				for _, o := range many {
					addOwner(o.Login)
				}
			}
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if !ok && len(owners) == 0 {
		return nil, e
	}
	return owners, nil
}

// checkRedirect notices that GitHub answered a request for owner/repo
// from another repo (the repo was renamed or transferred, and GitHub
// redirects the old name): it looks up the current name once and warns,
// in the daemon log, the log store and the install summary.
func (s *Source) checkRedirect(ctx context.Context, owner, repo, path string, resp *http.Response, tok string) {
	if resp.Request == nil || resp.Request.URL == nil {
		return
	}
	want, err := url.Parse(s.API + path)
	// Only a same-host redirect is a rename/transfer (tarballs redirect
	// to codeload.github.com, which is normal).
	if err != nil || resp.Request.URL.Path == want.Path || !strings.EqualFold(resp.Request.URL.Host, want.Host) {
		return
	}
	prefix := strings.TrimSuffix(strings.TrimSuffix(want.Path, strings.SplitN(path, "?", 2)[0]), "/")
	final := strings.TrimPrefix(resp.Request.URL.Path, prefix)
	key := strings.ToLower(owner + "/" + repo)
	s.mu.Lock()
	cur := s.redirects[key]
	s.mu.Unlock()
	if cur == "" { // only a found name is cached: a failed lookup is retried next time
		cur = currentName(final)
		if cur == "" && strings.HasPrefix(final, "/repositories/") {
			id := strings.SplitN(strings.TrimPrefix(final, "/repositories/"), "/", 2)[0]
			// Not tied to the request: a cancelled npm request must not cost the name.
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			if r, err := s.do(lctx, "/repositories/"+url.PathEscape(id), "application/vnd.github+json", tok); err == nil {
				var m struct {
					FullName string `json:"full_name"`
				}
				if r.StatusCode/100 == 2 {
					_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&m)
				}
				r.Body.Close()
				cur = m.FullName
			}
			cancel()
		}
		s.mu.Lock()
		if s.redirects == nil {
			s.redirects = map[string]string{}
		}
		if cur != "" {
			s.redirects[key] = cur
		} else {
			cur = s.redirects[key] // a concurrent lookup may have found it
		}
		s.mu.Unlock()
	}
	if strings.EqualFold(cur, owner+"/"+repo) {
		return
	}
	to := "another repo (GitHub redirects the old name)"
	if cur != "" {
		to = "github.com/" + cur
	}
	msg := fmt.Sprintf("github.com/%s/%s was renamed or transferred to %s; consona follows GitHub's redirect for now, "+
		"but update the dependency to the new name before the old one is reused or the redirect goes away", owner, repo, to)
	if cur != "" {
		if o, n, ok := strings.Cut(cur, "/"); ok {
			msg += fmt.Sprintf(" (@%s/%s)", strings.ToLower(o), strings.ToLower(n))
		}
	}
	core.Warn(core.RedirectKey(owner, repo), logstore.Entry{Event: logstore.EvSourceRedirect, Ecosystem: "github", Owner: owner, Repo: repo,
		Result: "redirected", HTTPStatus: resp.StatusCode, Message: msg, Fields: map[string]any{"requested": owner + "/" + repo, "current": cur}})
}

// currentName is "owner/name" when a redirected path is a /repos/ path.
func currentName(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		o, _ := url.PathUnescape(parts[1])
		n, _ := url.PathUnescape(parts[2])
		return o + "/" + n
	}
	return ""
}
