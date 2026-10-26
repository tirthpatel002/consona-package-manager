package npm

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/lookalike"
	"github.com/tirthpatel002/consona-package-manager/internal/procinfo"
)

func TestLookalikeWarningRecorded(t *testing.T) {
	routeScope(t, "o")
	t.Setenv("CONSONA_HOME", t.TempDir())
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/@o%2flib" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	start := time.Now()
	h := &handler{
		e:           core.NewEngine(&fakeSource{}, t.TempDir()),
		b:           fakeBuilder{},
		upstreamURL: "https://public.example",
		noWarm:      true,
	}
	daemonSrv := httptest.NewServer(h)
	h.base = daemonSrv.URL
	defer daemonSrv.Close()

	resp, err := http.Get(daemonSrv.URL + "/@o/lib")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Verify core.WarningsSince recorded the warning
	warnings := core.WarningsSince(start)
	if len(warnings) == 0 {
		t.Fatalf("expected lookalike warning, got none")
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "@o/lib also exists on the public npm registry") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("lookalike warning not found in %v", warnings)
	}
}

func TestLookalikeStrictMode(t *testing.T) {
	routeScope(t, "o")
	t.Setenv("CONSONA_HOME", t.TempDir())
	t.Setenv("CONSONA_STRICT", "1")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/@o%2flib" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	h := &handler{
		e:           core.NewEngine(&fakeSource{}, t.TempDir()),
		b:           fakeBuilder{},
		upstreamURL: "https://public.example",
		noWarm:      true,
	}
	daemonSrv := httptest.NewServer(h)
	h.base = daemonSrv.URL
	defer daemonSrv.Close()

	resp, err := http.Get(daemonSrv.URL + "/@o/lib")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden in strict mode, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp map[string]string
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("invalid json error: %v, body=%q", err, string(body))
	}
	msg := errResp["error"]
	if !strings.Contains(msg, "CONSONA_STRICT") || !strings.Contains(msg, "@o/lib") {
		t.Fatalf("error message does not mention CONSONA_STRICT and lookalike: %q", msg)
	}
}

func TestLookalikeStrictModePeerClientEnv(t *testing.T) {
	routeScope(t, "o")
	t.Setenv("CONSONA_HOME", t.TempDir())
	t.Setenv("CONSONA_STRICT", "") // not set in daemon env
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	// Mock peerLookup so the calling process has CONSONA_STRICT=1 in its env
	oldLookup := peerLookup
	peerLookup = func(local, remote net.Addr) (*procinfo.Process, error) {
		return &procinfo.Process{
			PID: 1234,
			Via: "mock",
			Env: map[string]string{"CONSONA_STRICT": "1"},
		}, nil
	}
	defer func() { peerLookup = oldLookup }()

	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	h := &handler{
		e:           core.NewEngine(&fakeSource{}, t.TempDir()),
		b:           fakeBuilder{},
		upstreamURL: "https://public.example",
		noWarm:      true,
	}
	daemonSrv := httptest.NewServer(h)
	h.base = daemonSrv.URL
	defer daemonSrv.Close()

	resp, err := http.Get(daemonSrv.URL + "/@o/lib")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden via peer env strict mode, got %d", resp.StatusCode)
	}
}

func TestLookalikeFailOpenInStrictMode(t *testing.T) {
	routeScope(t, "o")
	t.Setenv("CONSONA_HOME", t.TempDir())
	t.Setenv("CONSONA_STRICT", "1")
	lookalike.ResetDefault()
	defer lookalike.ResetDefault()

	var pubCalls atomic.Int32
	pubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pubCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // public registry fails with 500
	}))
	defer pubSrv.Close()
	t.Setenv("CONSONA_PUBLIC_REGISTRY", pubSrv.URL)

	h := &handler{
		e:           core.NewEngine(&fakeSource{}, t.TempDir()),
		b:           fakeBuilder{},
		upstreamURL: "https://public.example",
		noWarm:      true,
	}
	daemonSrv := httptest.NewServer(h)
	h.base = daemonSrv.URL
	defer daemonSrv.Close()

	resp, err := http.Get(daemonSrv.URL + "/@o/lib")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Must fail open: 200 OK packument served normally!
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected fail open 200 OK, got %d", resp.StatusCode)
	}
	if pubCalls.Load() != 1 {
		t.Fatalf("expected 1 call to public server, got %d", pubCalls.Load())
	}
}
