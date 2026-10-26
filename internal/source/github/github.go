// Package github is a core.Source backed by the GitHub API, authenticated
// with the token of the local gh login. The token is held in memory only.
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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// Source talks to the GitHub REST API.
type Source struct {
	API    string // e.g. https://api.github.com
	Token  string
	Client *http.Client
}

// New returns a Source using the token from `gh auth token`.
func New() (*Source, error) {
	tok, err := GHToken()
	if err != nil {
		return nil, err
	}
	return &Source{API: "https://api.github.com", Token: tok, Client: &http.Client{Timeout: 60 * time.Second}}, nil
}

// GHToken returns the gh login token (kept in memory by callers, never
// written). Order: GH_TOKEN/GITHUB_TOKEN (as gh itself honours them),
// `gh auth token` (gh >= 2.17), then `gh config get oauth_token` (older gh).
func GHToken() (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	for _, args := range [][]string{{"auth", "token"}, {"config", "get", "-h", "github.com", "oauth_token"}} {
		out, err := exec.Command("gh", args...).Output()
		if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
			return tok, nil
		}
	}
	return "", fmt.Errorf("not logged in to GitHub: run `gh auth login`")
}

func (s *Source) Describe(r core.Ref) string { return "github.com/" + r.String() }

func (s *Source) get(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.API+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 || resp.StatusCode == 451 {
		resp.Body.Close()
		return nil, core.ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
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
		resp, err := s.get(ctx, fmt.Sprintf("%s/tags?per_page=100&page=%d", repoPath(r), page), "application/vnd.github+json")
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
	resp, err := s.get(ctx, repoPath(r)+"/commits/HEAD", "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 100))
	return strings.TrimSpace(string(b)), err
}

func wrap(s *Source, r core.Ref, err error) error {
	if err == core.ErrNotFound {
		return fmt.Errorf("%s: %w", s.Describe(r), core.ErrNotFound)
	}
	return err
}

// Fetch downloads the commit tarball and extracts it into dir.
func (s *Source) Fetch(ctx context.Context, r core.Ref, commit, dir string) error {
	resp, err := s.get(ctx, repoPath(r)+"/tarball/"+url.PathEscape(commit), "application/vnd.github+json")
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

// Owners returns the logged-in user's login followed by their org logins.
func (s *Source) Owners(ctx context.Context) ([]string, error) {
	var owners []string
	for _, p := range []string{"/user", "/user/orgs?per_page=100"} {
		resp, err := s.get(ctx, p, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("GitHub %s: %w", p, err)
		}
		var one struct{ Login string }
		var many []struct{ Login string }
		if p == "/user" {
			err = json.NewDecoder(resp.Body).Decode(&one)
			owners = append(owners, one.Login)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&many)
			for _, o := range many {
				owners = append(owners, o.Login)
			}
		}
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	return owners, nil
}
