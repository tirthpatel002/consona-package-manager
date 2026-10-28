// Package core holds the ecosystem-neutral pieces of Consona: the plug-in
// interfaces (Source, Builder, Adapter), the version rules, and the Engine
// that ties a source to a builder with a commit-keyed artifact cache.
//
// New languages are added as new Builder + Adapter packages; new places to
// get code from are added as new Source packages. Nothing in core changes.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Ref identifies a private package by its code location. For GitHub the
// npm scope maps to Owner and the package name to Name.
type Ref struct {
	Owner string
	Name  string
}

func (r Ref) String() string { return r.Owner + "/" + r.Name }

// Revision is one buildable point in a source: a semver tag, or the default
// branch head when the repo has no version tags.
type Revision struct {
	Version string // semver without "v", derived from the tag (or 0.0.0-<sha>)
	Tag     string // original tag name; empty for the default-branch fallback
	Commit  string // full commit SHA
	Head    bool   // true for the untagged default-branch fallback
	// SourceID distinguishes revisions that share name and commit but not
	// content: a package mapped to another folder of the same repo keeps
	// the same commit, so without it the caches would serve the previous
	// folder's bytes. It is folded into every cache identity and is never
	// used as a fetch ref. Empty for sources whose name+commit is unique.
	SourceID string
}

// ErrNotFound means the package's source does not exist or is not
// accessible with the current credentials. It must never trigger a
// fallback to a public registry.
var ErrNotFound = errors.New("not found or not accessible")

// ErrAccess means no available credential could read the package source
// (rejected, expired, not authorized, rate limited). Errors that are
// ErrAccess carry a message naming each credential tried and how to fix
// it; when every credential just could not see the repo they are also
// ErrNotFound.
var ErrAccess = errors.New("no credential could read the source")

// Source lists versions of a package and fetches its source tree.
type Source interface {
	// Describe returns a human-readable location, e.g. "github.com/o/n".
	Describe(ref Ref) string
	// Revisions lists buildable revisions. Returns ErrNotFound (wrapped)
	// when the package does not exist or is not accessible.
	Revisions(ctx context.Context, ref Ref) ([]Revision, error)
	// Fetch extracts the source tree at commit into dir (which exists and is empty).
	Fetch(ctx context.Context, ref Ref, commit, dir string) error
}

// BuildRequest describes what a Builder should produce.
type BuildRequest struct {
	Name     string   // package name in the ecosystem, e.g. "@o/n"
	Version  string   // version to publish (already decided by the engine)
	Revision Revision // what is being built
	OutDir   string   // where to place the artifact
}

// Artifact is a built, cacheable package.
type Artifact struct {
	Version  string          `json:"version"`  // final version (may come from the package manifest)
	File     string          `json:"file"`     // artifact file name inside the cache dir
	Manifest json.RawMessage `json:"manifest"` // ecosystem manifest (e.g. package.json) as packed
	SHA1     string          `json:"sha1"`     // hex
	SHA512   string          `json:"sha512"`   // base64 (SRI form without prefix)
}

// Info is what a Builder reads from a source tree without building it.
type Info struct {
	Version    string          `json:"version"`    // declared version ("" if none)
	Manifest   json.RawMessage `json:"manifest"`   // declared manifest (e.g. package.json)
	NeedsBuild bool            `json:"needsBuild"` // has a build lifecycle; build lazily
}

// Builder turns a source tree into an ecosystem artifact.
type Builder interface {
	Ecosystem() string
	// Inspect reads the manifest in the source tree.
	Inspect(srcDir string) (*Info, error)
	// Build produces the artifact. Failures should be *BuildError when the
	// package's own build step failed, so its output can be shown.
	Build(ctx context.Context, srcDir string, req BuildRequest) (*Artifact, error)
}

// ArtifactNamer is an optional interface that a Builder can implement
// to provide the canonical artifact file name for a given package name and version.
type ArtifactNamer interface {
	FileName(name, version string) string
}

// ArtifactFileName returns the artifact file name Builder b produces for name and version.
// If b implements ArtifactNamer, it calls b.FileName; otherwise it falls back to
// the standard npm format (same as npmpack.FileName).
func ArtifactFileName(b Builder, name, version string) string {
	if an, ok := b.(ArtifactNamer); ok {
		return an.FileName(name, version)
	}
	return strings.NewReplacer("@", "", "/", "-").Replace(name) + "-" + version + ".tgz"
}

// BuildError is a failure of the package's own build step.
type BuildError struct {
	Step   string // e.g. "npm install", "npm run prepack"
	Script string // the lifecycle script that failed, e.g. `"prepare" script`; "" if unknown
	Output []byte // combined output of the failing step
}

func (e *BuildError) Error() string {
	if e.Script != "" {
		return e.Step + " failed: " + e.Script
	}
	return e.Step + " failed"
}

// Adapter integrates one package tool (npm, pnpm, pip, ...): it configures
// the tool once, serves the tool's registry protocol, and wraps its command.
type Adapter interface {
	Name() string
	// Setup configures the tool so the given scopes/owners route to baseURL.
	Setup(ctx context.Context, baseURL string, owners []string) (string, error)
	// Teardown undoes Setup, restoring the tool's previous configuration.
	Teardown(ctx context.Context) (string, error)
	// CheckSetup fails if Setup has not been run (private names would leak
	// to the public registry otherwise).
	CheckSetup(baseURL string) error
	// Handler serves the tool's protocol; it is mounted at /<Name()>/.
	Handler(e *Engine, baseURL string) http.Handler
	// Exec runs the native tool with args, passing stdio and exit code
	// through. baseURL is where Handler is mounted (adapters may use it to
	// warm the daemon while the tool starts up).
	Exec(ctx context.Context, baseURL string, args []string) int
}

type snapshotIDKey struct{}

// WithSnapshotID marks ctx as resolving under the project snapshot id, so
// version lists are never shared with live or other-snapshot requests.
func WithSnapshotID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, snapshotIDKey{}, id)
}

// SnapshotID returns the request's snapshot id, or "" for live resolution.
func SnapshotID(ctx context.Context) string {
	id, _ := ctx.Value(snapshotIDKey{}).(string)
	return id
}
