package npm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/procinfo"
)

// fakePeer replaces the OS lookup of the npm process behind a request.
func fakePeer(t *testing.T, fn func() (*procinfo.Process, error)) *int {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	old := peerLookup
	peerLookup = func(net.Addr, net.Addr) (*procinfo.Process, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return fn()
	}
	clear := func() { peerCache.Range(func(k, _ any) bool { peerCache.Delete(k); return true }) }
	clear()
	t.Cleanup(func() { peerLookup = old; clear() })
	return &calls
}

// captureLogs points the default log store at a temp JSON-lines store.
func captureLogs(t *testing.T) func(event string) []logstore.Entry {
	t.Helper()
	t.Setenv("CONSONA_LOG_BACKEND", "jsonl")
	s, err := logstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := logstore.NewAsync(s)
	logstore.SetDefault(a, false)
	t.Cleanup(func() { logstore.SetDefault(nil, false); a.Close() })
	return func(event string) []logstore.Entry {
		a.Flush(context.Background())
		es, _ := s.Query(logstore.Filter{Event: event})
		return es
	}
}

// upstreamAuth is a fake upstream registry recording each Authorization.
func upstreamAuth(t *testing.T, srv *httptest.Server) *[]string {
	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")+" otp="+r.Header.Get("npm-otp"))
		mu.Unlock()
		w.Write([]byte(`{"username":"x"}`))
	}))
	t.Cleanup(up.Close)
	srv.Config.Handler.(*handler).upstreamURL = up.URL
	return &seen
}

func whoami(t *testing.T, srv *httptest.Server, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/-/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+Placeholder)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The calling npm's project .npmrc (found by walking up from a nested
// cwd) with ${NPM_TOKEN} from npm's environment only, and its
// NPM_CONFIG_USERCONFIG, decide the login; OTP headers pass through.
func TestLoginFromCallingNpmProcess(t *testing.T) {
	srv, _ := newServer(t)
	seen := upstreamAuth(t, srv)
	up := srv.Config.Handler.(*handler).upstreamURL
	nerf := strings.TrimPrefix(up, "http:") + "/"
	d := t.TempDir()
	proj := filepath.Join(d, "proj")
	nested := filepath.Join(proj, "packages", "a", "src")
	os.MkdirAll(nested, 0o755)
	os.WriteFile(filepath.Join(proj, "package.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(proj, ".npmrc"), []byte(nerf+":_authToken=${NPM_TOKEN}\n"), 0o600)
	alt := filepath.Join(d, "alt-npmrc")
	os.WriteFile(alt, []byte(nerf+":_authToken=alt-user-token\n"), 0o600)
	if os.Getenv("NPM_TOKEN") != "" {
		t.Fatal("NPM_TOKEN must not be in the daemon's environment")
	}
	proc := &procinfo.Process{PID: 4242, UID: os.Getuid(), Env: map[string]string{"NPM_TOKEN": "project-token", "HOME": d}, Cwd: nested}
	fakePeer(t, func() (*procinfo.Process, error) { return proc, nil })
	logs := captureLogs(t)
	whoami(t, srv, map[string]string{"npm-otp": "123456"})
	peerCache.Range(func(k, _ any) bool { peerCache.Delete(k); return true }) // a new npm process
	proc.Env = map[string]string{"NPM_CONFIG_USERCONFIG": alt}
	proc.Cwd = d // no project
	whoami(t, srv, nil)
	want := []string{"/-/whoami Bearer project-token otp=123456", "/-/whoami Bearer alt-user-token otp="}
	if strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Fatalf("upstream saw %q, want %q", *seen, want)
	}
	if es := logs(logstore.EvPeerLookup); len(es) == 0 || es[0].Result != "ok" {
		t.Fatalf("peer.lookup entries: %+v", es)
	}
}

// A request from another user's process gets no login, and is logged.
func TestOtherUserProcessRefused(t *testing.T) {
	srv, _ := newServer(t)
	seen := upstreamAuth(t, srv)
	npmrc := os.Getenv("npm_config_userconfig")
	b, _ := os.ReadFile(npmrc)
	os.WriteFile(npmrc, append(b, []byte(strings.TrimPrefix(srv.Config.Handler.(*handler).upstreamURL, "http:")+"/:_authToken=daemon-user-token\n")...), 0o600)
	fakePeer(t, func() (*procinfo.Process, error) { return nil, &procinfo.OtherUserError{UID: os.Getuid() + 1} })
	logs := captureLogs(t)
	// No login: npm is sent to the registry itself (a redirect), nothing is forwarded.
	if code := whoami(t, srv, nil); code != http.StatusFound || len(*seen) != 0 {
		t.Fatalf("another user's request must not get a login: %d %q", code, *seen)
	}
	es := logs(logstore.EvPeerLookup)
	if len(es) != 1 || es[0].Result != "refused" || es[0].Level != "warn" {
		t.Fatalf("%+v", es)
	}
}

// If the npm process cannot be read, the daemon falls back to its own view
// (user npmrc), and says why in the log without any token value.
func TestUnresolvablePeerFallsBack(t *testing.T) {
	srv, _ := newServer(t)
	seen := upstreamAuth(t, srv)
	npmrc := os.Getenv("npm_config_userconfig")
	b, _ := os.ReadFile(npmrc)
	os.WriteFile(npmrc, append(b, []byte(strings.TrimPrefix(srv.Config.Handler.(*handler).upstreamURL, "http:")+"/:_authToken=daemon-user-token\n")...), 0o600)
	calls := fakePeer(t, func() (*procinfo.Process, error) {
		return nil, errors.New("kern.procargs2: operation not permitted")
	})
	logs := captureLogs(t)
	whoami(t, srv, map[string]string{"npm-otp": "654321"})
	if len(*seen) != 1 || (*seen)[0] != "/-/whoami Bearer daemon-user-token otp=654321" {
		t.Fatalf("fallback login: %q", *seen)
	}
	es := logs(logstore.EvPeerLookup)
	if len(es) != 1 || es[0].Result != "fallback" || es[0].Level != "warn" || !strings.Contains(es[0].Message, "operation not permitted") ||
		strings.Contains(es[0].Message, "daemon-user-token") {
		t.Fatalf("%+v", es)
	}
	if *calls != 1 {
		t.Fatalf("lookups: %d", *calls)
	}
}

// Plain installs of unscoped public packages from the default registry do
// not look up the npm process at all.
func TestPublicReadsSkipPeerLookup(t *testing.T) {
	srv, _ := newServer(t)
	srv.Config.Handler.(*handler).upstreamURL = strings.TrimRight(DefaultUpstream, "/")
	calls := fakePeer(t, func() (*procinfo.Process, error) { return nil, procinfo.ErrNotFound })
	for _, p := range []string{"/react", "/react/-/react-19.0.0.tgz"} {
		resp, err := noRedirect.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != strings.TrimRight(DefaultUpstream, "/")+p {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if *calls != 0 {
		t.Fatalf("peer lookups for public reads: %d", *calls)
	}
}
