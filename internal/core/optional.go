package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Prebuilt is optionally implemented by a Source whose revisions arrive as
// finished artifacts (e.g. packed by consona on an SSH remote): the engine
// then neither fetches nor builds them itself. ok=false means "not a
// prebuilt revision; fetch and build it as usual".
type Prebuilt interface {
	PrebuiltInfo(ctx context.Context, ref Ref, rev Revision) (info *Info, ok bool, err error)
	FetchPrebuilt(ctx context.Context, ref Ref, rev Revision, outDir string) (art *Artifact, ok bool, err error)
}

// Live is optionally implemented by a Source whose content changes without
// notice (a local folder): the engine re-resolves it on every request
// instead of reusing the version list for TTL. Builds stay cached by the
// revision's Commit, which for such sources is a content hash.
type Live interface {
	Live(ref Ref) bool
}

func (e *Engine) live(ref Ref) bool {
	l, ok := e.Source.(Live)
	return ok && l.Live(ref)
}

// FindCached returns the path of an already built artifact file of ref
// (any revision), or "". It lets a lockfile keep installing a version the
// source no longer lists (e.g. a local folder that has changed since).
func (e *Engine) FindCached(b Builder, ref Ref, file string) string {
	if file == "" || strings.ContainsAny(file, `/\`) || file == "." || file == ".." {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(e.base(b, ref), "*", "*", "artifact.json"))
	for _, m := range matches {
		var a Artifact
		if bs, err := os.ReadFile(m); err == nil && json.Unmarshal(bs, &a) == nil && a.File == file {
			p := filepath.Join(filepath.Dir(m), file)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}
