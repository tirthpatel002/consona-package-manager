package core

import (
	"reflect"
	"testing"
)

func TestTagVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"v1.2.0": "1.2.0", "1.2.0": "1.2.0", "v2.0.0-beta.1": "2.0.0-beta.1",
		"v1.2": "", "release-1": "", "v01.0.0": "", "latest": "",
	} {
		if got := TagVersion(tag); got != want {
			t.Errorf("TagVersion(%q)=%q want %q", tag, got, want)
		}
	}
}

func TestRevisionsFromTags(t *testing.T) {
	revs := RevisionsFromTags([]Tag{
		{"v1.10.0", "c3"}, {"junk", "c9"}, {"v1.2.0", "c1"}, {"1.2.0", "c1"}, {"v1.3.0-rc.1", "c2"},
	}, "head")
	var got []string
	for _, r := range revs {
		got = append(got, r.Version+"@"+r.Commit)
	}
	want := []string{"1.2.0@c1", "1.3.0-rc.1@c2", "1.10.0@c3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	// No semver tags: default branch head is the single version.
	revs = RevisionsFromTags([]Tag{{"junk", "c9"}}, "abcdef0123456789")
	if len(revs) != 1 || revs[0].Version != "0.0.0-abcdef0" || !revs[0].Head || revs[0].Commit != "abcdef0123456789" {
		t.Fatalf("head fallback: %+v", revs)
	}
}

func TestAssignVersions(t *testing.T) {
	revs := []Revision{{Version: "1.0.0", Commit: "a"}, {Version: "1.1.0", Commit: "b"}, {Version: "1.2.0", Commit: "c"}, {Version: "2.0.0", Commit: "d"}}
	// b forgot to bump (says 1.0.0, another tag's version) -> tag version.
	// c and d both claim 9.9.9 -> newest (d) wins, c uses its tag version.
	got := AssignVersions(revs, []string{"1.0.0", "1.0.0", "9.9.9", "9.9.9"})
	want := []string{"1.0.0", "1.1.0", "1.2.0", "9.9.9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	head := []Revision{HeadRevision("abcdef0123")}
	if got := AssignVersions(head, []string{"5.0.0"}); got[0] != "0.0.0-abcdef0" {
		t.Fatalf("head must keep 0.0.0-sha, got %v", got)
	}
}

func TestLatest(t *testing.T) {
	if got := Latest([]string{"1.0.0", "1.10.0", "2.0.0-rc.1", "1.9.0"}); got != "1.10.0" {
		t.Fatalf("got %s", got)
	}
	if got := Latest([]string{"0.0.0-abc1234"}); got != "0.0.0-abc1234" {
		t.Fatalf("got %s", got)
	}
}
