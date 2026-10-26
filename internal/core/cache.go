package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidCacheKey indicates a cache key does not conform to the required structure
// or attempts directory traversal.
var ErrInvalidCacheKey = errors.New("invalid cache key")

var (
	cacheEcosystemRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	cacheOwnerRe     = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	cacheRepoRe      = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	cacheCommitRe    = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	cacheVersionRe   = regexp.MustCompile(`^[a-zA-Z0-9_.+~-]+$`)
)

// BuildCacheKey returns the canonical build cache key for a package artifact:
// <ecosystem>/<owner>/<repo>/<commit>/<version>.
func BuildCacheKey(ecosystem, owner, repo, commit, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", ecosystem, owner, repo, commit, version)
}

// ValidateCacheKey parses and strictly validates a cache key.
// Allowed structure: exactly 5 components (<ecosystem>/<owner>/<repo>/<commit>/<version-or-file>)
// or 6 components (<ecosystem>/<owner>/<repo>/<commit>/<version>/<file>).
// Rejects directory traversal (..), absolute paths, path separators in components,
// and disallowed characters.
func ValidateCacheKey(key string) (ecosystem, owner, repo, commit, versionOrFile, file string, err error) {
	if key == "" {
		return "", "", "", "", "", "", fmt.Errorf("%w: empty key", ErrInvalidCacheKey)
	}
	if strings.HasPrefix(key, "/") || strings.HasPrefix(key, `\`) {
		return "", "", "", "", "", "", fmt.Errorf("%w: absolute key %q", ErrInvalidCacheKey, key)
	}
	parts := strings.Split(key, "/")
	if len(parts) != 5 && len(parts) != 6 {
		return "", "", "", "", "", "", fmt.Errorf("%w: expected 5 or 6 components, got %d in %q", ErrInvalidCacheKey, len(parts), key)
	}
	for i, p := range parts {
		if p == "" || p == "." || p == ".." || strings.Contains(p, "..") || strings.ContainsAny(p, `/\`+"\x00 \t\r\n") {
			return "", "", "", "", "", "", fmt.Errorf("%w: invalid component %d (%q)", ErrInvalidCacheKey, i, p)
		}
	}

	ecosystem, owner, repo, commit, versionOrFile = parts[0], parts[1], parts[2], parts[3], parts[4]
	if !cacheEcosystemRe.MatchString(ecosystem) {
		return "", "", "", "", "", "", fmt.Errorf("%w: invalid ecosystem %q", ErrInvalidCacheKey, ecosystem)
	}
	if !cacheOwnerRe.MatchString(owner) {
		return "", "", "", "", "", "", fmt.Errorf("%w: invalid owner %q", ErrInvalidCacheKey, owner)
	}
	if !cacheRepoRe.MatchString(repo) {
		return "", "", "", "", "", "", fmt.Errorf("%w: invalid repo %q", ErrInvalidCacheKey, repo)
	}
	if !cacheCommitRe.MatchString(commit) || len(commit) < 7 {
		return "", "", "", "", "", "", fmt.Errorf("%w: invalid commit %q", ErrInvalidCacheKey, commit)
	}
	if !cacheVersionRe.MatchString(versionOrFile) {
		return "", "", "", "", "", "", fmt.Errorf("%w: invalid version/file %q", ErrInvalidCacheKey, versionOrFile)
	}
	if len(parts) == 6 {
		file = parts[5]
		if !cacheVersionRe.MatchString(file) {
			return "", "", "", "", "", "", fmt.Errorf("%w: invalid file component %q", ErrInvalidCacheKey, file)
		}
	}
	return ecosystem, owner, repo, commit, versionOrFile, file, nil
}

// RemoteCacheResult carries the outcome of fetching an artifact from a remote cache.
type RemoteCacheResult struct {
	RemoteLabel string
	SHA512      string // base64 sha512 announced by the remote
	Size        int64
	Duration    time.Duration
}

// RemoteCache provides access to shared build cache artifacts over SSH.
//
// Integrity trust note:
// While an SSH remote is a machine explicitly configured by the user, artifacts
// fetched over SSH are verified end-to-end: the sha512 announced by the remote
// is checked against a freshly computed local sha512 sum, and the archive structure
// is verified (must be a valid gzip tar containing package/package.json with matching
// name) before installation. Furthermore, npm's lockfile integrity check also protects
// during installation.
type RemoteCache interface {
	Fetch(ctx context.Context, key string, outPath string) (res RemoteCacheResult, ok bool, err error)
}

// InspectTarball checks that tarballPath is a valid gzip tar archive containing
// package/package.json (or package.json) with a name matching expectedPkgName.
// Returns the raw package.json bytes.
func InspectTarball(tarballPath, expectedPkgName string) ([]byte, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("invalid gzip tarball: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var manifest []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid tar stream: %w", err)
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		clean = strings.TrimPrefix(clean, "./")
		if clean == "package/package.json" || clean == "package.json" {
			manifest, err = io.ReadAll(io.LimitReader(tr, 2<<20)) // 2MB limit
			if err != nil {
				return nil, fmt.Errorf("reading package.json: %w", err)
			}
			break
		}
	}
	if len(manifest) == 0 {
		return nil, fmt.Errorf("tarball missing package/package.json")
	}

	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifest, &meta); err != nil {
		return nil, fmt.Errorf("package.json in tarball is invalid: %w", err)
	}
	if expectedPkgName != "" && !strings.EqualFold(meta.Name, expectedPkgName) {
		return nil, fmt.Errorf("tarball package.json name %q does not match expected %q", meta.Name, expectedPkgName)
	}
	return manifest, nil
}
