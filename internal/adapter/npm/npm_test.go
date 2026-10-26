package npm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
)

// fakeSource knows exactly one repo, o/lib, with tag v1.0.0.
type fakeSource struct{ calls []string }

func (f *fakeSource) Describe(r core.Ref) string { return "fake/" + r.String() }
func (f *fakeSource) Revisions(_ context.Context, r core.Ref) ([]core.Revision, error) {
	f.calls = append(f.calls, r.String())
	if r.Name == "denied" {
		return nil, &github.AccessError{What: "github.com/o/denied", Owner: "o", Attempts: []github.Attempt{
			{Source: `named token "work" (github_pat_…ab12)`, Status: 401, Kind: github.KindExpired, Detail: "token expired on 2026-01-01 00:00 UTC", Fix: "create a new token at " + github.FineGrainedURL},
			{Source: "gh login (gho_…cd34)", Status: 403, Kind: github.KindSSO, Detail: "403: the organization uses SAML single sign-on", Fix: "authorize the token for SSO"},
		}}
	}
	if r != (core.Ref{Owner: "o", Name: "lib"}) {
		return nil, core.ErrNotFound
	}
	return core.RevisionsFromTags([]core.Tag{{Name: "v1.0.0", Commit: "1111111111"}}, ""), nil
}
func (f *fakeSource) Fetch(_ context.Context, _ core.Ref, _, dir string) error {
	return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@o/lib","version":"1.0.0"}`), 0o644)
}

// fakeBuilder writes a fixed tarball so the test needs no npm.
// With needsBuild it behaves like a package with a prepare script, and
// fail makes its build step fail.
type fakeBuilder struct{ needsBuild, fail bool }

func (fakeBuilder) Ecosystem() string { return "npm" }
func (f fakeBuilder) Inspect(string) (*core.Info, error) {
	return &core.Info{Version: "1.0.0", Manifest: []byte(`{"name":"@o/lib","dependencies":{"@o/other":"^1"}}`), NeedsBuild: f.needsBuild}, nil
}
func (f fakeBuilder) Build(_ context.Context, _ string, r core.BuildRequest) (*core.Artifact, error) {
	if f.fail {
		return nil, &core.BuildError{Step: "npm install", Output: []byte("tsc: error TS2322")}
	}
	data := []byte("tarball-bytes")
	file := npmpack.FileName(r.Name, r.Version)
	if err := os.WriteFile(filepath.Join(r.OutDir, file), data, 0o644); err != nil {
		return nil, err
	}
	s1, s512 := npmpack.Hashes(data)
	return &core.Artifact{Version: r.Version, File: file, Manifest: []byte(`{"name":"@o/lib","dependencies":{"@o/other":"^1"}}`), SHA1: s1, SHA512: s512}, nil
}

func newServer(t *testing.T, fb ...fakeBuilder) (*httptest.Server, *fakeSource) {
	routeScope(t, "o")
	src := &fakeSource{}
	e := core.NewEngine(src, t.TempDir())
	h := &handler{e: e, b: fakeBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	if len(fb) > 0 {
		h.b = fb[0]
	}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	t.Cleanup(srv.Close)
	return srv, src
}

// routeScope points a temp user npmrc's consona block at the given scopes.
func routeScope(t *testing.T, scopes ...string) string {
	t.Setenv("CONSONA_HOME", t.TempDir())
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	out, _ := UpdateNpmrc("", "http://127.0.0.1:1/npm/", scopes)
	os.WriteFile(npmrc, []byte(out), 0o600)
	t.Setenv("npm_config_userconfig", npmrc)
	return npmrc
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestPackumentAndTarballIntegrity(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/@o%2flib")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("packument: %v %v", err, resp.Status)
	}
	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Deps map[string]string `json:"dependencies"`
			Dist struct{ Tarball, Shasum, Integrity string }
		}
	}
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	v := doc.Versions["1.0.0"]
	if doc.DistTags["latest"] != "1.0.0" || v.Deps["@o/other"] != "^1" {
		t.Fatalf("bad packument %+v", doc)
	}
	tr, err := http.Get(v.Dist.Tarball)
	if err != nil || tr.StatusCode != 200 {
		t.Fatalf("tarball: %v", err)
	}
	defer tr.Body.Close()
	data, _ := io.ReadAll(tr.Body)
	s1, s512 := npmpack.Hashes(data)
	if v.Dist.Shasum != s1 || v.Dist.Integrity != "sha512-"+s512 {
		t.Fatal("dist hashes do not match served tarball")
	}
}

// A routed private name that does not exist is a clear 404 and is never
// looked up anywhere else (the handler has no upstream to fall back to).
// Credential failures reach npm as a 403 (not retried) whose message names
// every credential tried and how to fix it.
func TestCredentialErrorReachesNpm(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/@o%2fdenied")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	msg := body["error"]
	for _, want := range []string{"@o/denied is private", `named token "work" (github_pat_…ab12): token expired`, github.FineGrainedURL, "gh login (gho_…cd34): 403", "authorize the token for SSO"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %d %q", want, resp.StatusCode, msg)
		}
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMissingPrivateNameNoFallback(t *testing.T) {
	srv, src := newServer(t)
	for _, p := range []string{"/@o%2fmissing", "/@o/missing/-/o-missing-1.0.0.tgz"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != 404 || !strings.Contains(body["error"], "not found or is not accessible with your GitHub credentials") {
			t.Fatalf("%s: %d %v", p, resp.StatusCode, body)
		}
	}
	// Public names (unscoped or other scopes) are redirected upstream
	// without touching the source; nothing public passes through consona.
	before := len(src.calls)
	for p, want := range map[string]string{
		"/react":                         "https://public.example/react",
		"/react/-/react-18.3.1.tgz":      "https://public.example/react/-/react-18.3.1.tgz",
		"/@types%2freact":                "https://public.example/@types%2freact",
		"/-/v1/search?text=react&size=2": "https://public.example/-/v1/search?text=react&size=2",
	} {
		resp, _ := noRedirect.Get(srv.URL + p)
		resp.Body.Close()
		if resp.StatusCode != 302 || resp.Header.Get("Location") != want {
			t.Fatalf("%s: %d -> %s", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if len(src.calls) != before {
		t.Fatal("public names must not reach the source")
	}
	// Anything else naming a private scope stays local.
	for _, p := range []string{"/-/package/@o%2flib/dist-tags", "/-/v1/search?text=%40o%2Flib"} {
		resp, _ := noRedirect.Get(srv.URL + p)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("%s must not be redirected: %d", p, resp.StatusCode)
		}
	}
}

// Writes and authenticated reads are forwarded as-is to the upstream with
// the user's upstream login (read from the npmrc, never the placeholder);
// anonymous reads are redirects; private-scope writes are refused locally.
func TestUpstreamForwardingWithAuth(t *testing.T) {
	type seen struct{ method, path, auth, otp, body string }
	var got []seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("npm-otp"), string(b)})
		w.WriteHeader(201)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	srv, _ := newServer(t)
	srv.Config.Handler.(*handler).upstreamURL = up.URL

	do := func(method, path, auth string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"name":"pkg"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("npm-otp", "123456")
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// No upstream login configured: anonymous reads redirect, writes forward.
	if r := do("GET", "/-/whoami", "Bearer "+Placeholder); r.StatusCode != 302 {
		t.Fatalf("anonymous read: %d", r.StatusCode)
	}
	if r := do("PUT", "/pkg", "Bearer "+Placeholder); r.StatusCode != 201 || got[0].auth != "" || got[0].body != `{"name":"pkg"}` || got[0].otp != "123456" {
		t.Fatalf("write: %d %+v", r.StatusCode, got)
	}
	// With a login for the upstream in the npmrc, the placeholder becomes it.
	npmrc := os.Getenv("npm_config_userconfig")
	b, _ := os.ReadFile(npmrc)
	os.WriteFile(npmrc, append(b, []byte(strings.TrimPrefix(up.URL, "http:")+"/:_authToken=${UP_TOKEN}\n")...), 0o600)
	t.Setenv("UP_TOKEN", "s3cret")
	got = nil
	for _, c := range []struct{ m, p string }{{"GET", "/-/whoami"}, {"GET", "/@acme%2fsecret"}, {"PUT", "/-/package/pkg/dist-tags/beta"}, {"DELETE", "/-/user/token/abc"}} {
		if r := do(c.m, c.p, "Bearer "+Placeholder); r.StatusCode != 201 {
			t.Fatalf("%s %s: %d", c.m, c.p, r.StatusCode)
		}
	}
	for _, g := range got {
		if g.auth != "Bearer s3cret" {
			t.Fatalf("upstream got auth %q for %s", g.auth, g.path)
		}
	}
	if got[1].path != "/@acme%2fsecret" {
		t.Fatalf("path not preserved: %s", got[1].path)
	}
	// A real token (login made through consona) passes through unchanged.
	got = nil
	do("GET", "/-/whoami", "Bearer real-login")
	if got[0].auth != "Bearer real-login" {
		t.Fatalf("got %q", got[0].auth)
	}
	// Private scope: never forwarded.
	got = nil
	if r := do("PUT", "/@o%2flib", "Bearer "+Placeholder); r.StatusCode != 403 || len(got) != 0 {
		t.Fatalf("private publish: %d, upstream saw %v", r.StatusCode, got)
	}
}

func TestUpdateNpmrc(t *testing.T) {
	in := "registry=https://mirror.example/\n@abc:registry=https://evil.example/\n"
	out, err := UpdateNpmrc(in, "http://127.0.0.1:1/npm/", []string{"ABC", "@org"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ = UpdateNpmrc(out, "http://127.0.0.1:1/npm/", []string{"extra"}) // idempotent, keeps scopes
	want := "registry=https://mirror.example/\n" + blockStart + "\nregistry=http://127.0.0.1:1/npm/\n//127.0.0.1:1/npm/:_authToken=" + Placeholder +
		"\nfetch-retry-mintimeout=500\nfetch-retry-maxtimeout=2000\n@abc:registry=http://127.0.0.1:1/npm/\n@extra:registry=http://127.0.0.1:1/npm/\n@org:registry=http://127.0.0.1:1/npm/\n" + blockEnd + "\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

// npm rewrites the npmrc without comments (npm login / config set): the
// private scopes must still be recognised, and undo must still restore.
func TestSurvivesNpmRewrite(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	orig := "registry=https://mirror.example/\nfund=false\n"
	os.WriteFile(npmrc, []byte(orig), 0o600)
	t.Setenv("npm_config_userconfig", npmrc)
	base := "http://127.0.0.1:1/npm/"
	(Adapter{}).Setup(context.Background(), base, []string{"o"})
	// What npm writes after `npm login` through consona: comments gone,
	// duplicate registry collapsed, placeholder replaced by the new token.
	os.WriteFile(npmrc, []byte("registry="+base+"\nfund=false\n//127.0.0.1:1/npm/:_authToken=npm_login\nfetch-retry-mintimeout=500\nfetch-retry-maxtimeout=2000\n@o:registry="+base+"\n"), 0o600)
	if !RoutedScopes()["o"] || (Adapter{}).CheckSetup(base) != nil || Upstream() != "https://mirror.example" {
		t.Fatal("consona config lost after npm rewrite")
	}
	(Adapter{}).Teardown(context.Background())
	b, _ := os.ReadFile(npmrc)
	if string(b) != "fund=false\nregistry=https://mirror.example/\n//mirror.example/:_authToken=npm_login\n" {
		t.Fatalf("undo after rewrite:\n%s", b)
	}
}

// Packages with a build step are listed without hashes and built when
// their tarball is requested; a failed build is one clear 422 naming the
// package and version, with the output saved to a log file.
func TestLazyBuildAndBuildFailure(t *testing.T) {
	srv, _ := newServer(t, fakeBuilder{needsBuild: true})
	doc := getJSON(t, srv.URL+"/@o%2flib")
	dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
	if dist["integrity"] != nil {
		t.Fatalf("unbuilt version must not claim an integrity: %v", dist)
	}
	resp, err := http.Get(dist["tarball"].(string))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("lazy tarball: %v %v", err, resp.Status)
	}
	resp.Body.Close()

	srv, _ = newServer(t, fakeBuilder{needsBuild: true, fail: true})
	resp, _ = http.Get(srv.URL + "/@o/lib/-/o-lib-1.0.0.tgz")
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	msg := body["error"]
	if resp.StatusCode != 422 || !strings.Contains(msg, "build failed for @o/lib@1.0.0") || !strings.Contains(msg, "build-failed.log") {
		t.Fatalf("%d %q", resp.StatusCode, msg)
	}
	logf := msg[strings.LastIndex(msg, " ")+1:]
	if b, _ := os.ReadFile(logf); !strings.Contains(string(b), "TS2322") {
		t.Fatalf("build output not saved in %s", logf)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

// Teardown restores the npmrc as it was before setup, and Upstream is the
// previous registry.
func TestTeardownRestores(t *testing.T) {
	t.Setenv("CONSONA_HOME", t.TempDir())
	orig := "registry=https://mirror.example/\ncolor=false\n"
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	os.WriteFile(npmrc, []byte(orig), 0o600)
	t.Setenv("npm_config_userconfig", npmrc)
	if _, err := (Adapter{}).Setup(context.Background(), "http://127.0.0.1:1/npm/", []string{"o"}); err != nil {
		t.Fatal(err)
	}
	if err := (Adapter{}).CheckSetup("http://127.0.0.1:1/npm/"); err != nil {
		t.Fatal(err)
	}
	if u := Upstream(); u != "https://mirror.example" {
		t.Fatalf("upstream %q", u)
	}
	(Adapter{}).Teardown(context.Background())
	if b, _ := os.ReadFile(npmrc); string(b) != orig {
		t.Fatalf("not restored:\n%s", b)
	}
	if Upstream()+"/" != DefaultUpstream {
		t.Fatal("default upstream")
	}
}

// Audits forward only public names; private names never reach upstream.
func TestAuditWithholdsPrivateNames(t *testing.T) {
	routeScope(t, "o")
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"react":[]}`))
	}))
	defer up.Close()
	h := &handler{e: core.NewEngine(&fakeSource{}, t.TempDir()), b: fakeBuilder{}, upstreamURL: up.URL, noWarm: true}
	srv := httptest.NewServer(h)
	defer srv.Close()

	post := func(path, body string) {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		zw.Write([]byte(body))
		zw.Close()
		req, _ := http.NewRequest("POST", srv.URL+path, &gz)
		req.Header.Set("Content-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
	}
	post(bulkPath, `{"react":["18.3.1"],"@o/lib":["1.0.0"],"@other/pub":["1.0.0"]}`)
	if _, leak := got["@o/lib"]; leak || got["react"] == nil || got["@other/pub"] == nil {
		t.Fatalf("bulk upstream received %v", got)
	}
	post(quickPath, `{"name":"@o/app","requires":{"@o/lib":"^1","react":"^18"},"dependencies":{"@o/lib":{"version":"1.0.0"},"react":{"version":"18.3.1","requires":{"@o/x":"1"},"dependencies":{"@o/x":{"version":"1"}}}}}`)
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "@o/") || !strings.Contains(string(b), "react") {
		t.Fatalf("quick upstream received %s", b)
	}
}
