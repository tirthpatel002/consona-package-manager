package sshremote

import (
	"bufio"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/identity"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
)

// Server answers `consona remote-serve` requests from this machine's local
// index only.
type Server struct {
	Version string // consona version
	Index   *localindex.Store
	Local   *local.Source
	Engine  *core.Engine // over Local
	Builder core.Builder
}

// Serve handles request lines from in until EOF, answering on out.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	br := bufio.NewReader(in)
	bw := bufio.NewWriter(out)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var req Request
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				s.reply(bw, Response{Error: "bad request: " + jerr.Error()})
			} else {
				s.handle(ctx, req, bw)
			}
			if ferr := bw.Flush(); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Server) reply(w io.Writer, r Response) {
	r.Protocol = Protocol
	b, _ := json.Marshal(r)
	w.Write(append(b, '\n'))
}

func (s *Server) handle(ctx context.Context, req Request, w io.Writer) {
	switch req.Op {
	case "hello", "list":
		r := Response{OK: true, Consona: s.Version}
		if id, err := identity.Load(); err == nil {
			r.Username, r.Device = id.Username, &id.Device
		}
		if req.Op == "list" {
			es, err := s.Index.List()
			if err != nil {
				s.reply(w, Response{Error: err.Error()})
				return
			}
			for _, e := range es {
				r.Packages = append(r.Packages, Summary{Name: e.Name})
			}
		}
		s.reply(w, r)
	case "resolve":
		p, _, err := s.resolve(ctx, req.Name)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: errors.Is(err, core.ErrNotFound)})
			return
		}
		s.reply(w, Response{OK: true, Package: p})
	case "fetch":
		path, err := s.artifactPath(ctx, req.Name, req.Version)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: errors.Is(err, core.ErrNotFound)})
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			s.reply(w, Response{Error: err.Error()})
			return
		}
		sum := sha512.Sum512(data)
		s.reply(w, Response{OK: true, Size: int64(len(data)), SHA512: base64.StdEncoding.EncodeToString(sum[:])})
		w.Write(data)
	case "cache_has":
		_, art, tarballPath, err := s.resolveCacheEntry(req.Key)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: errors.Is(err, core.ErrNotFound)})
			return
		}
		st, err := os.Stat(tarballPath)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: true})
			return
		}
		s.reply(w, Response{OK: true, Size: st.Size(), SHA512: art.SHA512})
	case "cache_fetch":
		_, art, tarballPath, err := s.resolveCacheEntry(req.Key)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: errors.Is(err, core.ErrNotFound)})
			return
		}
		data, err := os.ReadFile(tarballPath)
		if err != nil {
			s.reply(w, Response{Error: err.Error(), NotFound: true})
			return
		}
		name := ""
		if len(art.Manifest) > 0 {
			var m struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(art.Manifest, &m)
			name = m.Name
		}
		sum := sha512.Sum512(data)
		p := &Package{
			Name:     name,
			Version:  art.Version,
			File:     art.File,
			Manifest: art.Manifest,
			SHA1:     art.SHA1,
			SHA512:   art.SHA512,
			Size:     int64(len(data)),
		}
		s.reply(w, Response{OK: true, Package: p, Size: int64(len(data)), SHA512: base64.StdEncoding.EncodeToString(sum[:])})
		w.Write(data)
	default:
		s.reply(w, Response{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

// registered maps a requested name to its index entry; anything not
// registered here is "not found" (the server never serves other files).
func (s *Server) registered(name string) (core.Ref, string, error) {
	scope, pkg, err := localindex.SplitName(strings.ToLower(name))
	if err != nil {
		return core.Ref{}, "", err
	}
	e, ok, err := s.Index.Lookup(name)
	if err != nil {
		return core.Ref{}, "", err
	}
	if !ok {
		return core.Ref{}, "", fmt.Errorf("%s: %w on this remote", name, core.ErrNotFound)
	}
	return core.Ref{Owner: scope, Name: pkg}, e.Name, nil
}

// resolve packs (or reuses) the current content of a registered package.
func (s *Server) resolve(ctx context.Context, name string) (*Package, *core.Built, error) {
	ref, n, err := s.registered(name)
	if err != nil {
		return nil, nil, err
	}
	pkgs, err := s.Engine.Packages(ctx, s.Builder, ref, n)
	if err != nil {
		return nil, nil, err
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("%s: %w", n, core.ErrNotFound)
	}
	v := pkgs[len(pkgs)-1]
	bl := v.Built
	if bl == nil {
		if bl, err = s.Engine.Build(ctx, s.Builder, ref, n, v); err != nil {
			return nil, nil, err
		}
	}
	st, err := os.Stat(bl.Path)
	if err != nil {
		return nil, nil, err
	}
	return &Package{
		Name: n, Version: v.Version, Hash: strings.TrimPrefix(v.Revision.Commit, local.CommitPrefix),
		File: bl.Artifact.File, Manifest: bl.Artifact.Manifest, SHA1: bl.Artifact.SHA1, SHA512: bl.Artifact.SHA512, Size: st.Size(),
	}, bl, nil
}

// artifactPath is the built tarball of name@version: the current content,
// or an earlier build still in the cache.
func (s *Server) artifactPath(ctx context.Context, name, version string) (string, error) {
	ref, n, err := s.registered(name)
	if err != nil {
		return "", err
	}
	if !core.ValidVersion(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	p, bl, err := s.resolve(ctx, n)
	if err == nil && p.Version == version {
		return bl.Path, nil
	}
	if f := s.Engine.FindCached(s.Builder, ref, npmpack.FileName(n, version)); f != "" {
		return f, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s@%s: %w (current is %s)", n, version, core.ErrNotFound, p.Version)
}

func (s *Server) resolveCacheEntry(key string) (string, *core.Artifact, string, error) {
	if s.Engine == nil || s.Engine.CacheDir == "" {
		return "", nil, "", fmt.Errorf("build cache unavailable: %w", core.ErrNotFound)
	}
	ecosystem, owner, repo, commit, versionOrFile, file, err := core.ValidateCacheKey(key)
	if err != nil {
		return "", nil, "", err
	}
	cacheDir := s.Engine.CacheDir
	cleanCache := filepath.Clean(cacheDir)

	var entryDir string
	var expectedFile string
	if file != "" {
		entryDir = filepath.Join(cacheDir, ecosystem, owner, repo, commit, versionOrFile)
		expectedFile = file
	} else if strings.HasSuffix(versionOrFile, ".tgz") {
		expectedFile = versionOrFile
		commitDir := filepath.Join(cacheDir, ecosystem, owner, repo, commit)
		entries, _ := os.ReadDir(commitDir)
		for _, e := range entries {
			if e.IsDir() {
				candidate := filepath.Join(commitDir, e.Name())
				if _, err := os.Stat(filepath.Join(candidate, "artifact.json")); err == nil {
					entryDir = candidate
					break
				}
			}
		}
		if entryDir == "" {
			return "", nil, "", fmt.Errorf("cache entry for %s: %w", key, core.ErrNotFound)
		}
	} else {
		entryDir = filepath.Join(cacheDir, ecosystem, owner, repo, commit, versionOrFile)
	}

	cleanEntry := filepath.Clean(entryDir)
	rel, err := filepath.Rel(cleanCache, cleanEntry)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", nil, "", fmt.Errorf("invalid path traversal: %w", core.ErrInvalidCacheKey)
	}

	artJSONPath := filepath.Join(cleanEntry, "artifact.json")
	artBytes, err := os.ReadFile(artJSONPath)
	if err != nil {
		return "", nil, "", fmt.Errorf("artifact metadata %s: %w", key, core.ErrNotFound)
	}
	var art core.Artifact
	if err := json.Unmarshal(artBytes, &art); err != nil {
		return "", nil, "", fmt.Errorf("invalid artifact metadata: %w", err)
	}

	tarballName := art.File
	if expectedFile != "" && expectedFile != art.File {
		tarballName = expectedFile
	}
	if tarballName == "" || strings.ContainsAny(tarballName, `/\`) || tarballName == "." || tarballName == ".." {
		return "", nil, "", fmt.Errorf("invalid artifact file name: %w", core.ErrNotFound)
	}

	tarballPath := filepath.Join(cleanEntry, tarballName)
	cleanTarball := filepath.Clean(tarballPath)
	relTar, err := filepath.Rel(cleanCache, cleanTarball)
	if err != nil || strings.HasPrefix(relTar, "..") || relTar == "." {
		return "", nil, "", fmt.Errorf("invalid path traversal: %w", core.ErrInvalidCacheKey)
	}

	if _, err := os.Stat(cleanTarball); err != nil {
		return "", nil, "", fmt.Errorf("artifact tarball missing: %w", core.ErrNotFound)
	}

	return cleanEntry, &art, cleanTarball, nil
}
