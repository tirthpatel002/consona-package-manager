// Package npmpack builds npm tarballs from a source tree the way npm builds
// a git dependency: if the package has a build lifecycle (prepare/prepack),
// install its dependencies including devDependencies (which runs prepare),
// run prepack, then `npm pack`. `files`, .npmignore and the always-included
// files behave exactly as for a normal publish.
package npmpack

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// Builder implements core.Builder for npm.
type Builder struct{}

func (Builder) Ecosystem() string { return "npm" }

type manifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Scripts map[string]string `json:"scripts"`
}

func read(dir string) ([]byte, *manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("no package.json at the repository root")
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, fmt.Errorf("invalid package.json: %w", err)
	}
	return b, &m, nil
}

// needsBuild mirrors when npm runs a build for a git dependency.
func needsBuild(m *manifest) bool { return m.Scripts["prepare"] != "" || m.Scripts["prepack"] != "" }

// Inspect reads package.json.
func (Builder) Inspect(dir string) (*core.Info, error) {
	raw, m, err := read(dir)
	if err != nil {
		return nil, err
	}
	var c bytes.Buffer
	if err := json.Compact(&c, raw); err != nil {
		return nil, err
	}
	return &core.Info{Version: m.Version, Manifest: c.Bytes(), NeedsBuild: needsBuild(m)}, nil
}

// FileName is the tarball name npm pack produces for name@version.
func FileName(name, version string) string {
	return strings.NewReplacer("@", "", "/", "-").Replace(name) + "-" + version + ".tgz"
}

// FileName implements core.ArtifactNamer for Builder.
func (Builder) FileName(name, version string) string {
	return FileName(name, version)
}

// Build packs dir into req.OutDir with name/version forced to the request.
func (Builder) Build(ctx context.Context, dir string, req core.BuildRequest) (*core.Artifact, error) {
	_, m, err := read(dir)
	if err != nil {
		return nil, err
	}
	var set []string
	if m.Name != req.Name {
		set = append(set, "name="+req.Name)
	}
	if m.Version != req.Version {
		set = append(set, "version="+req.Version)
	}
	if len(set) > 0 {
		if err := npm(ctx, dir, false, append([]string{"pkg", "set"}, set...)...); err != nil {
			return nil, err
		}
	}
	if needsBuild(m) {
		// Like npm for git deps: full install incl. devDependencies; this
		// runs the package's own prepare. Then prepack, then pack.
		if err := npm(ctx, dir, true, "install", "--include=dev", "--no-save", "--no-audit", "--no-fund", "--loglevel=error"); err != nil {
			return nil, err
		}
		if m.Scripts["prepack"] != "" {
			if err := npm(ctx, dir, true, "run", "prepack"); err != nil {
				return nil, err
			}
		}
	}
	if err := npm(ctx, dir, false, "pack", "--ignore-scripts", "--silent", "--pack-destination", req.OutDir); err != nil {
		return nil, err
	}
	file := FileName(req.Name, req.Version)
	data, err := os.ReadFile(filepath.Join(req.OutDir, file))
	if err != nil {
		return nil, fmt.Errorf("npm pack produced no %s: %w", file, err)
	}
	raw, _, err := read(dir)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	s1, s512 := Hashes(data)
	return &core.Artifact{Version: req.Version, File: file, Manifest: compact.Bytes(), SHA1: s1, SHA512: s512}, nil
}

// Hashes returns the hex sha1 (npm "shasum") and base64 sha512 (SRI body).
func Hashes(data []byte) (string, string) {
	a := sha1.Sum(data)
	b := sha512.Sum512(data)
	return hex.EncodeToString(a[:]), base64.StdEncoding.EncodeToString(b[:])
}

// npm runs one npm step; failures carry the step's output as a BuildError.
func npm(ctx context.Context, dir string, scripts bool, args ...string) error {
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false")
	if !scripts {
		cmd.Env = append(cmd.Env, "npm_config_ignore_scripts=true")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(&out, "\n[consona] npm %s in %s: %v\n", strings.Join(args, " "), dir, err)
		return &core.BuildError{Step: "npm " + strings.Join(args[:min(2, len(args))], " "), Script: failedScript(args, out.Bytes(), dir), Output: out.Bytes()}
	}
	return nil
}

// npm prints "> name@version <event>" before it runs a lifecycle script
// of the package itself, and "npm error path <dir>" for the package
// whose script failed.
var (
	scriptBanner = regexp.MustCompile(`(?m)^> \S+@\S+ ([a-z][a-z0-9:_-]*)\s*$`)
	errorPath    = regexp.MustCompile(`(?m)^npm (?:error|ERR!) path (.+?)\s*$`)
)

// failedScript names the lifecycle script that failed in an npm step, as
// shown to the user: `"prepare" script`, `"prepack" script`, or an install
// script of a dependency. "" when the failure was not a script.
func failedScript(args []string, out []byte, dir string) string {
	if len(args) >= 2 && args[0] == "run" {
		return strconv.Quote(args[1]) + " script"
	}
	if len(args) == 0 || args[0] != "install" {
		return ""
	}
	if m := errorPath.FindAllSubmatch(out, -1); len(m) > 0 {
		p := filepath.Clean(string(m[len(m)-1][1]))
		if rel, err := filepath.Rel(dir, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return "install script of dependency " + filepath.ToSlash(rel)
		}
	}
	if m := scriptBanner.FindAllSubmatch(out, -1); len(m) > 0 {
		return strconv.Quote(string(m[len(m)-1][1])) + " script"
	}
	return ""
}
