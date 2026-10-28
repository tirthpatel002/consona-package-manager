package npm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/snapshot"
)

// snapSource resolves like source/github: under a snapshot it reads the
// recorded tags, otherwise the live (mutable) list. It counts live reads.
type snapSource struct {
	tagSource
	live int
}

func (s *snapSource) Revisions(ctx context.Context, ref core.Ref) ([]core.Revision, error) {
	if snap, _, ok := snapshot.FromContext(ctx); ok {
		tags, head, err := snap.TagsAndHead(ref)
		if err != nil {
			return nil, err
		}
		return core.RevisionsFromTags(tags, head), nil
	}
	s.live++
	return s.tagSource.Revisions(ctx, ref)
}

func snapServer(t *testing.T, src *snapSource) *httptest.Server {
	t.Helper()
	routeScope(t, "o")
	t.Setenv("CONSONA_CONFIG_DIR", t.TempDir())
	e := core.NewEngine(src, t.TempDir())
	e.TTL = 0 // memo isolation is tested through the key, not through expiry
	e.TTL = 1 << 40
	h := &handler{e: e, b: commitBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func storeSnap(t *testing.T, tags map[string]string) string {
	t.Helper()
	data, err := snapshot.Marshal(&snapshot.Snapshot{Version: 1, Scopes: []string{"o"},
		Repos: map[string]snapshot.Repo{"o/lib": {Head: recCommitA, Tags: tags}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := snapshot.Save(data)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func versionsOf(t *testing.T, url string) (int, []string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	var vs []string
	for v := range doc.Versions {
		vs = append(vs, v)
	}
	return resp.StatusCode, vs
}

func TestSnapshotFreezesVersionsWhileLiveMoves(t *testing.T) {
	src := &snapSource{tagSource: tagSource{tags: []core.Tag{{Name: "v1.0.0", Commit: recCommitA}}}}
	srv := snapServer(t, src)
	hash := storeSnap(t, map[string]string{"v1.0.0": recCommitA})
	u := srv.URL + "/s/" + hash + "/@o%2flib"

	code, vs := versionsOf(t, u)
	if code != 200 || len(vs) != 1 || vs[0] != "1.0.0" {
		t.Fatalf("snapshot packument: %d %v", code, vs)
	}
	// The repo moves on: a new release appears live.
	src.tags = append(src.tags, core.Tag{Name: "v1.1.0", Commit: recCommitB})
	code, vs = versionsOf(t, u)
	if code != 200 || len(vs) != 1 {
		t.Fatalf("snapshot must not see the new live tag: %d %v", code, vs)
	}
	if src.live != 0 {
		t.Fatalf("snapshot request read live state %d time(s)", src.live)
	}
	// A plain request shares no memo with the snapshot one.
	code, vs = versionsOf(t, srv.URL+"/@o%2flib")
	if code != 200 || len(vs) != 2 || src.live != 1 {
		t.Fatalf("plain request must resolve live: %d %v live=%d", code, vs, src.live)
	}
	// And the snapshot is still frozen afterwards (the live memo did not leak into it).
	if _, vs = versionsOf(t, u); len(vs) != 1 {
		t.Fatalf("live memo leaked into the snapshot: %v", vs)
	}
}

func TestSnapshotTwoProjectsDoNotShareState(t *testing.T) {
	src := &snapSource{tagSource: tagSource{tags: []core.Tag{{Name: "v9.0.0", Commit: recCommitB}}}}
	srv := snapServer(t, src)
	h1 := storeSnap(t, map[string]string{"v1.0.0": recCommitA})
	h2 := storeSnap(t, map[string]string{"v1.0.0": recCommitA, "v1.1.0": recCommitB})
	_, a := versionsOf(t, srv.URL+"/s/"+h1+"/@o%2flib")
	_, b := versionsOf(t, srv.URL+"/s/"+h2+"/@o%2flib")
	_, a2 := versionsOf(t, srv.URL+"/s/"+h1+"/@o%2flib")
	if len(a) != 1 || len(b) != 2 || len(a2) != 1 {
		t.Fatalf("projects mixed: %v %v %v", a, b, a2)
	}
}

func TestSnapshotFailsClosed(t *testing.T) {
	src := &snapSource{tagSource: tagSource{tags: []core.Tag{{Name: "v1.0.0", Commit: recCommitA}}}}
	srv := snapServer(t, src)
	good := storeSnap(t, map[string]string{"v1.0.0": recCommitA})

	for name, tc := range map[string]struct {
		path string
		want int
	}{
		"unloaded id":    {"/s/" + strings.Repeat("a", 64) + "/@o%2flib", 404},
		"malformed id":   {"/s/not-a-hash/@o%2flib", 400},
		"traversal id":   {"/s/../@o%2flib", 400},
		"repo not in it": {"/s/" + good + "/@o%2fother", 404},
	} {
		code, _ := getBody(t, srv.URL+tc.path)
		if code != tc.want && !(name == "repo not in it" && code >= 400) {
			t.Errorf("%s: got %d want %d", name, code, tc.want)
		}
	}
	if src.live != 0 {
		t.Fatalf("a failed snapshot request fell back to live state (%d reads)", src.live)
	}
}
