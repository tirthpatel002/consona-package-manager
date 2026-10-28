package npmpack

import (
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestQualifiedFileNameRoundTrip(t *testing.T) {
	name := "@acme/widgets-core"
	for _, rev := range []core.Revision{
		{Commit: commitA},
		{Commit: commitA, SourceID: "ghfolder/acme/widgets/packages/core"},
		{Commit: commitB, SourceID: "ghfolder/acme/widgets/packages/ui"},
	} {
		for _, version := range []string{"1.2.0", "0.0.0-8fd5bab", "2.0.0-rc.1"} {
			f := QualifiedFileName(name, version, rev)
			v, id, ok := ParseQualifiedFileName(name, f)
			if !ok || v != version || id != core.CacheID(rev) {
				t.Fatalf("round trip %q: %q %q %v", f, v, id, ok)
			}
		}
	}
}

func TestQualifiedFileNameOnlyForFullCommits(t *testing.T) {
	// Live local folders (and short refs) keep the plain name: no commit
	// pins them.
	for _, commit := range []string{"", "local-h2cab0880", "abc123"} {
		if f := QualifiedFileName("@o/x", "1.0.0", core.Revision{Commit: commit}); f != "o-x-1.0.0.tgz" {
			t.Fatalf("commit %q must not qualify: %q", commit, f)
		}
	}
}

func TestParseQualifiedFileNameRejects(t *testing.T) {
	name := "@acme/widgets-core"
	for _, f := range []string{
		"acme-widgets-core-1.2.0.tgz",                                        // plain FileName: not qualified
		"acme-widgets-core-1.2.0-abc123.tgz",                                 // short ref
		"acme-widgets-core-1.2.0-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.tgz", // uppercase hex
		"acme-widgets-core-notsemver-" + commitA + ".tgz",                     // invalid version
		"acme-widgets-core-1.2.0-" + commitA + "-srczz.tgz",                   // non-hex source hash
		"other-pkg-1.2.0-" + commitA + ".tgz",                                 // another package's name
		"acme-widgets-core-1.2.0-" + commitA + ".tgz/evil",                     // suffix junk
	} {
		if v, id, ok := ParseQualifiedFileName(name, f); ok {
			t.Fatalf("%q must not parse (got %q %q)", f, v, id)
		}
	}
}

func TestParseQualifiedFileNameVersionEndingInHex(t *testing.T) {
	// A pathological version ending in a 40-hex prerelease still splits at
	// the identity (the version group is greedy).
	version := "1.0.0-" + commitB
	f := QualifiedFileName("@o/x", version, core.Revision{Commit: commitA})
	v, id, ok := ParseQualifiedFileName("@o/x", f)
	if !ok || v != version || id != commitA {
		t.Fatalf("got %q %q %v from %q", v, id, ok, f)
	}
}

func TestQualifiedFileNameDiffersPerFolder(t *testing.T) {
	// Two folders of one repo at one commit: the names must differ, or the
	// old URL could serve a remapped folder's bytes.
	a := QualifiedFileName("@acme/widgets-core", "1.0.0", core.Revision{Commit: commitA, SourceID: "ghfolder/acme/widgets/packages/core"})
	b := QualifiedFileName("@acme/widgets-core", "1.0.0", core.Revision{Commit: commitA, SourceID: "ghfolder/acme/widgets/packages/renamed"})
	if a == b {
		t.Fatalf("remapped folder must change the tarball name: %q", a)
	}
	if !strings.HasPrefix(a, "acme-widgets-core-1.0.0-"+commitA+"-src") {
		t.Fatalf("unexpected name %q", a)
	}
}
