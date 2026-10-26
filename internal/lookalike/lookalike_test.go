package lookalike

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

func TestCheckerStatus200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/@acme%2futils" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusExists {
		t.Fatalf("expected StatusExists, got %v", status)
	}
}

func TestCheckerStatus404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusNotExists {
		t.Fatalf("expected StatusNotExists, got %v", status)
	}
}

func TestCheckerStatus500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusUnknown {
		t.Fatalf("expected StatusUnknown, got %v", status)
	}
}

func TestCheckerTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()
	c.Timeout = 50 * time.Millisecond // very short timeout for test

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusUnknown {
		t.Fatalf("expected StatusUnknown on timeout, got %v", status)
	}
}

func TestCheckerNoAuthHeaders(t *testing.T) {
	var checkedHeaders bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedHeaders = true
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("unexpected Authorization header: %q", auth)
		}
		if cookie := r.Header.Get("Cookie"); cookie != "" {
			t.Errorf("unexpected Cookie header: %q", cookie)
		}
		if sess := r.Header.Get("npm-session"); sess != "" {
			t.Errorf("unexpected npm-session header: %q", sess)
		}
		if ua := r.Header.Get("User-Agent"); ua != "consona" {
			t.Errorf("expected User-Agent 'consona', got %q", ua)
		}
		if esc := r.URL.EscapedPath(); esc != "/@acme%2futils" {
			t.Errorf("expected escaped path /@acme%%2futils, got %q", esc)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusExists {
		t.Fatalf("expected StatusExists, got %v", status)
	}
	if !checkedHeaders {
		t.Fatalf("server was not called")
	}
}

func TestCheckerMethodFallback(t *testing.T) {
	var headCalled, getCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			headCalled = true
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			getCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/utils")
	if status != StatusExists {
		t.Fatalf("expected StatusExists, got %v", status)
	}
	if !headCalled || !getCalled {
		t.Fatalf("expected both HEAD and GET to be called, got head=%v get=%v", headCalled, getCalled)
	}
}

func TestCheckerCacheHitAndExpiry(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	homeDir := t.TempDir()
	curTime := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return curTime }

	c1 := New()
	c1.RegistryURL = srv.URL
	c1.HomeDir = homeDir
	c1.Now = clock

	// 1. First check: calls server
	s1 := c1.Check(context.Background(), "@acme/utils")
	if s1 != StatusExists {
		t.Fatalf("s1: expected StatusExists, got %v", s1)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}

	// Verify lookalike.json was written to disk
	diskPath := filepath.Join(homeDir, "lookalike.json")
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("lookalike.json not found on disk: %v", err)
	}

	// 2. Second check at now + 1 hour: hits memory cache
	curTime = curTime.Add(1 * time.Hour)
	s2 := c1.Check(context.Background(), "@acme/utils")
	if s2 != StatusExists {
		t.Fatalf("s2: expected StatusExists, got %v", s2)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected cache hit (1 call), got %d", callCount.Load())
	}

	// 3. New checker instance at now + 2 hours: hits disk cache
	curTime = curTime.Add(1 * time.Hour)
	c2 := New()
	c2.RegistryURL = srv.URL
	c2.HomeDir = homeDir
	c2.Now = clock

	s3 := c2.Check(context.Background(), "@acme/utils")
	if s3 != StatusExists {
		t.Fatalf("s3: expected StatusExists, got %v", s3)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected disk cache hit (1 call), got %d", callCount.Load())
	}

	// 4. Advance time past 24h (T0 + 25h): cache expired, server is called again
	curTime = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Add(25 * time.Hour)
	s4 := c2.Check(context.Background(), "@acme/utils")
	if s4 != StatusExists {
		t.Fatalf("s4: expected StatusExists, got %v", s4)
	}
	if callCount.Load() != 2 {
		t.Fatalf("expected server call after expiry (2 calls), got %d", callCount.Load())
	}
}

func TestCheckerNegativeCacheHit(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	s1 := c.Check(context.Background(), "@acme/safe")
	if s1 != StatusNotExists {
		t.Fatalf("expected StatusNotExists, got %v", s1)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}

	// Immediate repeat should hit memory cache
	s2 := c.Check(context.Background(), "@acme/safe")
	if s2 != StatusNotExists {
		t.Fatalf("expected StatusNotExists, got %v", s2)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call (negative cache hit), got %d", callCount.Load())
	}
}

func TestCheckerUnknownNotCached(t *testing.T) {
	var callCount atomic.Int32
	var statusToReturn atomic.Int32
	statusToReturn.Store(http.StatusInternalServerError)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(int(statusToReturn.Load()))
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	// 1. Returns 500 => StatusUnknown
	s1 := c.Check(context.Background(), "@acme/flaky")
	if s1 != StatusUnknown {
		t.Fatalf("expected StatusUnknown, got %v", s1)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}

	// 2. Change server to return 200: next check must NOT be cached
	statusToReturn.Store(http.StatusOK)
	s2 := c.Check(context.Background(), "@acme/flaky")
	if s2 != StatusExists {
		t.Fatalf("expected StatusExists, got %v", s2)
	}
	if callCount.Load() != 2 {
		t.Fatalf("expected server called again because unknown was not cached, got %d calls", callCount.Load())
	}
}

func TestCheckerFailOpenLogsDebugEvent(t *testing.T) {
	var loggedEntry atomic.Pointer[logstore.Entry]
	remove := logstore.AddTee(func(e logstore.Entry) {
		if e.Event == logstore.EvPublicLookalikeCheck {
			loggedEntry.Store(&e)
		}
	})
	defer remove()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New()
	c.RegistryURL = srv.URL
	c.HomeDir = t.TempDir()

	status := c.Check(context.Background(), "@acme/skipped")
	if status != StatusUnknown {
		t.Fatalf("expected StatusUnknown, got %v", status)
	}

	time.Sleep(50 * time.Millisecond)
	entry := loggedEntry.Load()
	if entry == nil {
		t.Fatalf("expected EvPublicLookalikeCheck to be logged")
	}
	if entry.Result != "skipped" || entry.Level != "debug" || entry.Package != "@acme/skipped" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestCheckerPublicRegistryEnv(t *testing.T) {
	t.Setenv("CONSONA_PUBLIC_REGISTRY", "http://custom-registry.test:8080/")
	defer ResetDefault()

	if reg := PublicRegistry(); reg != "http://custom-registry.test:8080" {
		t.Fatalf("expected trailing slash stripped, got %q", reg)
	}
	if host := RegistryHost(PublicRegistry()); host != "custom-registry.test:8080" {
		t.Fatalf("expected host custom-registry.test:8080, got %q", host)
	}
}
