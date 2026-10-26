package npm

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/devsync"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/procinfo"
)

func TestDevProjectTrackingWhenTarballServed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONSONA_HOME", home)

	projDir := t.TempDir()

	logs := captureLogs(t)
	fakePeer(t, func() (*procinfo.Process, error) {
		return &procinfo.Process{
			PID: 1234,
			Cwd: projDir,
			Env: map[string]string{
				"npm_config_local_prefix": projDir,
			},
		}, nil
	})

	ExtraScopes = func() map[string]bool { return map[string]bool{"me": true} }
	defer func() { ExtraScopes = nil }()

	src := &liveSource{ver: "1.0.0-local.1.haaaaaaaa"}
	h := &handler{e: core.NewEngine(src, t.TempDir()), b: fakeBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	defer srv.Close()

	// 1. Packument request: should NOT track dev project (not a tarball)
	resp, err := srv.Client().Get(srv.URL + "/@me%2flib")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	projs, err := devsync.GetTrackedProjects(home, "@me/lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 0 {
		t.Fatalf("expected 0 tracked projects after packument request, got %d", len(projs))
	}

	// 2. Tarball request: should track dev project
	resp, err = srv.Client().Get(srv.URL + "/@me/lib/-/me-lib-1.0.0-local.1.haaaaaaaa.tgz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tarball fetch returned %d", resp.StatusCode)
	}

	projs, err = devsync.GetTrackedProjects(home, "@me/lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 1 {
		t.Fatalf("expected 1 tracked project after tarball request, got %d", len(projs))
	}
	if projs[0].Dir != projDir {
		t.Fatalf("expected dir %s, got %s", projDir, projs[0].Dir)
	}

	// Verify logged event
	es := logs(logstore.EvDevProjectTracked)
	if len(es) == 0 {
		t.Fatalf("expected %s event logged", logstore.EvDevProjectTracked)
	}
	if es[0].Package != "@me/lib" {
		t.Fatalf("expected package @me/lib, got %s", es[0].Package)
	}
	if es[0].Level != "debug" {
		t.Fatalf("expected debug level, got %s", es[0].Level)
	}
}

func TestDevProjectTracking_NonLiveSourceNotTracked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CONSONA_HOME", home)

	projDir := t.TempDir()
	fakePeer(t, func() (*procinfo.Process, error) {
		return &procinfo.Process{
			PID: 1234,
			Cwd: projDir,
		}, nil
	})

	routeScope(t, "o")
	// fakeSource is not a Live source
	h := &handler{e: core.NewEngine(&fakeSource{}, t.TempDir()), b: fakeBuilder{}, upstreamURL: "https://public.example", noWarm: true}
	srv := httptest.NewServer(h)
	h.base = srv.URL
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/@o/x/-/x-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	projs, err := devsync.GetTrackedProjects(home, "@o/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 0 {
		t.Fatalf("expected 0 tracked projects for non-live source, got %d", len(projs))
	}
}
