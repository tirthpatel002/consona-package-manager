package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/creds"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// A renamed repo: GitHub answers the old name with a 301 to
// /repositories/<id>/...; consona follows it and warns once (log store
// event, daemon log) naming the new repo, and every time in the install
// summary. Plain requests and cross-host redirects (tarballs to codeload)
// are not warnings.
func TestRenamedRepoRedirectWarns(t *testing.T) {
	testRenamedRepoRedirect(t, false)
}

// The first name lookup fails: the warning says "another repo" and the
// next request looks the name up again (nothing empty is cached), so the
// latest warning (the one the install summary repeats) names the new repo.
func TestRenamedRepoLookupRetried(t *testing.T) {
	testRenamedRepoRedirect(t, true)
}

func testRenamedRepoRedirect(t *testing.T, failFirstLookup bool) {
	t.Setenv("CONSONA_LOG_BACKEND", "jsonl")
	st, err := logstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := logstore.NewAsync(st)
	logstore.SetDefault(a, false)
	t.Cleanup(func() { logstore.SetDefault(nil, false); a.Close() })

	var lookups atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tarball")) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/old/tags":
			http.Redirect(w, r, "/repositories/42/tags?"+r.URL.RawQuery, http.StatusMovedPermanently)
		case "/repositories/42/tags", "/repos/o/new/tags", "/repos/o/plain/tags":
			w.Write([]byte(`[]`))
		case "/repositories/42":
			if failFirstLookup && lookups.Add(1) == 1 {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			w.Write([]byte(`{"id":42,"full_name":"o/new"}`))
		case "/repos/o/plain/tarball/abc":
			http.Redirect(w, r, other.URL+"/codeload/o/plain/abc", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &creds.Resolver{Getenv: func(k string) string { return map[string]string{"CONSONA_TOKEN": tokGood}[k] }, TTL: time.Minute}
	s := &Source{API: srv.URL, Creds: r, Client: srv.Client()}
	ctx := context.Background()
	start := time.Now()
	get := func(repo, path string) {
		t.Helper()
		resp, err := s.get(ctx, "o", repo, path, "application/vnd.github+json")
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
	}
	get("plain", "/repos/o/plain/tags?per_page=100")
	get("plain", "/repos/o/plain/tarball/abc")
	if w := core.WarningsSince(start); len(w) != 0 {
		t.Fatalf("no redirect, but warnings %q", w)
	}
	get("old", "/repos/o/old/tags?per_page=100")
	if failFirstLookup {
		if w := core.WarningsSince(start); len(w) != 1 || !strings.Contains(w[0], "renamed or transferred to another repo") {
			t.Fatalf("failed lookup: warnings %q", w)
		}
		start = time.Now()
	}
	get("old", "/repos/o/old/tags?per_page=100&page=2")
	w := core.WarningsSince(start)
	if failFirstLookup {
		w = w[len(w)-1:]
	}
	if len(w) != 1 || !strings.Contains(w[0], "github.com/o/old was renamed or transferred to github.com/o/new") || !strings.Contains(w[0], "@o/new") {
		t.Fatalf("warnings %q", w)
	}
	a.Flush(ctx)
	es, _ := st.Query(logstore.Filter{Event: logstore.EvSourceRedirect})
	if failFirstLookup { // the log store event is once per key per WarnLogEvery (global): checked by the other test
		return
	}
	if len(es) != 1 || es[0].Level != "warn" || es[0].Fields["current"] != "o/new" || es[0].Repo != "old" {
		t.Fatalf("redirect events %+v", es)
	}
	for _, e := range es {
		noTokens(t, e.Message)
	}
}
