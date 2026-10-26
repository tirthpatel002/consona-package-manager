package core

import (
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Full semver 2.0 (major.minor.patch required), optional leading "v".
var semverRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// TagVersion returns the semver (without "v") for a tag like "v1.2.0" or
// "1.2.0", or "" if the tag is not a full semver.
func TagVersion(tag string) string {
	if !semverRe.MatchString(tag) {
		return ""
	}
	return strings.TrimPrefix(tag, "v")
}

// ValidVersion reports whether v is a full semver without a "v" prefix.
func ValidVersion(v string) bool { return v != "" && v[0] != 'v' && semverRe.MatchString(v) }

// CompareVersions compares two semvers (without "v").
func CompareVersions(a, b string) int { return semver.Compare("v"+a, "v"+b) }

// Tag is a git tag name and the commit it points to.
type Tag struct {
	Name   string
	Commit string
}

// RevisionsFromTags turns tags into revisions: non-semver tags are ignored,
// duplicates ("v1.0.0" and "1.0.0") collapse, result sorted ascending.
// If no tag is a semver, it returns the default-branch fallback at head.
func RevisionsFromTags(tags []Tag, head string) []Revision {
	seen := map[string]bool{}
	var out []Revision
	for _, t := range tags {
		v := TagVersion(t.Name)
		if v == "" || seen[v] || t.Commit == "" {
			continue
		}
		seen[v] = true
		out = append(out, Revision{Version: v, Tag: t.Name, Commit: t.Commit})
	}
	if len(out) == 0 {
		if head == "" {
			return nil
		}
		return []Revision{HeadRevision(head)}
	}
	sort.Slice(out, func(i, j int) bool { return CompareVersions(out[i].Version, out[j].Version) < 0 })
	return out
}

// HeadRevision is the single version used when a repo has no version tags.
func HeadRevision(commit string) Revision {
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	return Revision{Version: "0.0.0-" + short, Commit: commit, Head: true}
}

// AssignVersions picks the published version for each revision (same
// order as revs). Tags decide which commits are versions; the manifest
// version at a tag (e.g. package.json "version") is used when it is a valid
// semver that equals the tag's own version or collides with no other tag
// and no earlier pick. Otherwise the tag version is used. Head (untagged)
// revisions always keep 0.0.0-<sha>. The result never has duplicates.
func AssignVersions(revs []Revision, manifest []string) []string {
	out := make([]string, len(revs))
	tagVers := map[string]bool{}
	for _, r := range revs {
		tagVers[r.Version] = true
	}
	used := map[string]bool{}
	// Newest tag first so it wins a contested manifest version.
	idx := make([]int, len(revs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return CompareVersions(revs[idx[a]].Version, revs[idx[b]].Version) > 0 })
	for _, i := range idx {
		r, m := revs[i], ""
		if i < len(manifest) {
			m = manifest[i]
		}
		v := r.Version
		if !r.Head && ValidVersion(m) && !used[m] && (m == r.Version || !tagVers[m]) {
			v = m
		}
		used[v] = true
		out[i] = v
	}
	return out
}

// Latest returns the highest non-prerelease version, or the highest overall.
func Latest(versions []string) string {
	best, bestRel := "", ""
	for _, v := range versions {
		if best == "" || CompareVersions(v, best) > 0 {
			best = v
		}
		if semver.Prerelease("v"+v) == "" && (bestRel == "" || CompareVersions(v, bestRel) > 0) {
			bestRel = v
		}
	}
	if bestRel != "" {
		return bestRel
	}
	return best
}
