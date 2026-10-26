package core

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mockRemoteCache struct {
	fetchFunc func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error)
}

func (m *mockRemoteCache) Fetch(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
	if m.fetchFunc != nil {
		return m.fetchFunc(ctx, key, outPath)
	}
	return RemoteCacheResult{}, false, nil
}

type trackBuilder struct {
	builtCount int
	shouldFail bool
}

func (trackBuilder) Ecosystem() string { return "npm" }
func (trackBuilder) Inspect(string) (*Info, error) {
	return &Info{Version: "1.0.0", Manifest: []byte(`{"name":"@o/x"}`), NeedsBuild: true}, nil
}
func (trackBuilder) FileName(name, version string) string {
	return strings.NewReplacer("@", "", "/", "-").Replace(name) + "-" + version + ".tgz"
}
func (b *trackBuilder) Build(_ context.Context, _ string, r BuildRequest) (*Artifact, error) {
	b.builtCount++
	if b.shouldFail {
		return nil, &BuildError{Step: "build", Output: []byte("failed")}
	}
	tarPath := filepath.Join(r.OutDir, "o-x-1.0.0.tgz")
	createTestTarball(nil, tarPath, `{"name":"@o/x","version":"1.0.0"}`)
	data, _ := os.ReadFile(tarPath)
	sum := sha512.Sum512(data)
	return &Artifact{
		Version:  r.Version,
		File:     "o-x-1.0.0.tgz",
		Manifest: []byte(`{"name":"@o/x"}`),
		SHA1:     "localsha1",
		SHA512:   base64.StdEncoding.EncodeToString(sum[:]),
	}, nil
}

func TestEngineUsesRemoteArtifactAndDoesNotBuild(t *testing.T) {
	cacheDir := t.TempDir()
	src := &oneTagSource{commit: "1111111111222222222233333333334444444444"}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// Setup remote cache that returns a valid tarball
	remotePayloadDir := t.TempDir()
	remoteTarball := filepath.Join(remotePayloadDir, "remote.tgz")
	createTestTarball(t, remoteTarball, `{"name":"@o/x","version":"1.0.0"}`)
	remoteBytes, err := os.ReadFile(remoteTarball)
	if err != nil {
		t.Fatal(err)
	}
	sum512 := sha512.Sum512(remoteBytes)
	announced512 := base64.StdEncoding.EncodeToString(sum512[:])

	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			if err := os.WriteFile(outPath, remoteBytes, 0o644); err != nil {
				return RemoteCacheResult{}, false, err
			}
			return RemoteCacheResult{
				RemoteLabel: "laptop (10.0.0.2)",
				SHA512:      announced512,
				Size:        int64(len(remoteBytes)),
				Duration:    800 * time.Millisecond,
			}, true, nil
		},
	}

	ref := Ref{Owner: "o", Name: "x"}
	v := &Version{
		Revision: Revision{Version: "1.0.0", Commit: "1111111111222222222233333333334444444444"},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: true},
	}

	built, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if tb.builtCount != 0 {
		t.Fatalf("Builder.Build was called %d times; want 0 (should use remote cache)", tb.builtCount)
	}

	if built == nil || built.Artifact == nil {
		t.Fatal("Build returned nil artifact")
	}
	if built.Artifact.SHA512 != announced512 {
		t.Fatalf("Artifact SHA512 = %q, want %q", built.Artifact.SHA512, announced512)
	}

	// Verify artifact file and artifact.json exist on disk in the engine cache
	installedDir := filepath.Join(cacheDir, "npm", "o", "x", v.Revision.Commit, "1.0.0")
	if _, err := os.Stat(filepath.Join(installedDir, "artifact.json")); err != nil {
		t.Fatalf("artifact.json missing: %v", err)
	}
	if _, err := os.Stat(built.Path); err != nil {
		t.Fatalf("artifact file missing at %s: %v", built.Path, err)
	}

	// Calling Build again should return from local disk cache without touching remote cache
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			t.Fatal("RemoteCache should not be called when artifact is locally cached")
			return RemoteCacheResult{}, false, nil
		},
	}
	built2, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}
	if built2.Artifact.SHA512 != announced512 {
		t.Fatalf("second Build artifact sha512 = %q", built2.Artifact.SHA512)
	}
}

func TestClientFetchSha512MismatchFallbackToLocalBuild(t *testing.T) {
	cacheDir := t.TempDir()
	src := &oneTagSource{commit: "2222222222333333333344444444445555555555"}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// Remote returns payload with a mismatched announced sha512
	remotePayloadDir := t.TempDir()
	remoteTarball := filepath.Join(remotePayloadDir, "remote.tgz")
	createTestTarball(t, remoteTarball, `{"name":"@o/x","version":"1.0.0"}`)
	remoteBytes, _ := os.ReadFile(remoteTarball)

	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			_ = os.WriteFile(outPath, remoteBytes, 0o644)
			return RemoteCacheResult{
				RemoteLabel: "laptop",
				SHA512:      "mismatched-sha512-value",
				Size:        int64(len(remoteBytes)),
				Duration:    500 * time.Millisecond,
			}, true, nil
		},
	}

	ref := Ref{Owner: "o", Name: "x"}
	v := &Version{
		Revision: Revision{Version: "1.0.0", Commit: "2222222222333333333344444444445555555555"},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: true},
	}

	built, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should have fallen back to local builder
	if tb.builtCount != 1 {
		t.Fatalf("Builder.Build called %d times; want 1 (fallback after mismatch)", tb.builtCount)
	}
	if built.Artifact.SHA1 != "localsha1" {
		t.Fatalf("built artifact SHA1 = %q, want localsha1", built.Artifact.SHA1)
	}
}

func TestOfflineRemoteFailOpen(t *testing.T) {
	cacheDir := t.TempDir()
	src := &oneTagSource{commit: "3333333333444444444455555555556666666666"}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// Remote is offline / error
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			return RemoteCacheResult{}, false, errors.New("ssh: connect to host laptop: Connection timed out")
		},
	}

	ref := Ref{Owner: "o", Name: "x"}
	v := &Version{
		Revision: Revision{Version: "1.0.0", Commit: "3333333333444444444455555555556666666666"},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: true},
	}

	built, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("Build should fail open on remote offline, but returned error: %v", err)
	}

	if tb.builtCount != 1 {
		t.Fatalf("Builder.Build called %d times; want 1", tb.builtCount)
	}
	if built == nil {
		t.Fatal("built is nil")
	}
}

func TestRemoteCacheMissFallbackToLocalBuild(t *testing.T) {
	cacheDir := t.TempDir()
	src := &oneTagSource{commit: "4444444444555555555566666666667777777777"}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// Remote has a cache miss (ok: false, err: nil)
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			return RemoteCacheResult{}, false, nil
		},
	}

	ref := Ref{Owner: "o", Name: "x"}
	v := &Version{
		Revision: Revision{Version: "1.0.0", Commit: "4444444444555555555566666666667777777777"},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: true},
	}

	built, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if tb.builtCount != 1 {
		t.Fatalf("Builder.Build called %d times; want 1 (fallback on miss)", tb.builtCount)
	}
	if built == nil {
		t.Fatal("built is nil")
	}
}

func TestNoBuildPackageNeverCallsRemoteCacheFetch(t *testing.T) {
	cacheDir := t.TempDir()
	src := &oneTagSource{commit: "5555555555666666666677777777778888888888"}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// RemoteCache should FAIL if called
	fetchCalled := false
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			fetchCalled = true
			t.Fatal("RemoteCache.Fetch must not be called for packages without a build step")
			return RemoteCacheResult{}, false, nil
		},
	}

	ref := Ref{Owner: "o", Name: "x"}

	// Case 1: v.Info != nil but v.Info.NeedsBuild is false
	vNoBuild := &Version{
		Revision: Revision{Version: "1.0.0", Commit: "5555555555666666666677777777778888888888"},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: false},
	}

	built, err := e.Build(context.Background(), tb, ref, "@o/x", vNoBuild)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if fetchCalled {
		t.Fatal("RemoteCache.Fetch was called for package with NeedsBuild=false")
	}
	if tb.builtCount != 1 {
		t.Fatalf("Builder.Build should be called locally; got %d calls", tb.builtCount)
	}
	if built == nil {
		t.Fatal("expected non-nil Built")
	}

	// Case 2: v.Info == nil
	tb.builtCount = 0
	vNilInfo := &Version{
		Revision: Revision{Version: "2.0.0", Commit: "6666666666777777777788888888889999999999"},
		Version:  "2.0.0",
		Info:     nil,
	}

	built2, err := e.Build(context.Background(), tb, ref, "@o/x", vNilInfo)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if fetchCalled {
		t.Fatal("RemoteCache.Fetch was called for package with Info=nil")
	}
	if tb.builtCount != 1 {
		t.Fatalf("Builder.Build should be called locally; got %d calls", tb.builtCount)
	}
	if built2 == nil {
		t.Fatal("expected non-nil Built")
	}
}

func TestRemoteHitSecondBuildIsLocalCacheHit(t *testing.T) {
	cacheDir := t.TempDir()
	commit := "7777777777888888888899999999990000000000"
	src := &oneTagSource{commit: commit}
	e := NewEngine(src, cacheDir)

	tb := &trackBuilder{}

	// Setup remote payload
	remotePayloadDir := t.TempDir()
	remoteTarball := filepath.Join(remotePayloadDir, "remote.tgz")
	createTestTarball(t, remoteTarball, `{"name":"@o/x","version":"1.0.0"}`)
	remoteBytes, err := os.ReadFile(remoteTarball)
	if err != nil {
		t.Fatal(err)
	}
	sum512 := sha512.Sum512(remoteBytes)
	announced512 := base64.StdEncoding.EncodeToString(sum512[:])

	fetchCount := 0
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			fetchCount++
			if err := os.WriteFile(outPath, remoteBytes, 0o644); err != nil {
				return RemoteCacheResult{}, false, err
			}
			return RemoteCacheResult{
				RemoteLabel: "laptop",
				SHA512:      announced512,
				Size:        int64(len(remoteBytes)),
				Duration:    500 * time.Millisecond,
			}, true, nil
		},
	}

	ref := Ref{Owner: "o", Name: "x"}
	v := &Version{
		Revision: Revision{Version: "1.0.0", Commit: commit},
		Version:  "1.0.0",
		Info:     &Info{NeedsBuild: true},
	}

	// 1. First Build: remote cache hit
	built1, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}
	if fetchCount != 1 {
		t.Fatalf("expected 1 fetch call, got %d", fetchCount)
	}
	if tb.builtCount != 0 {
		t.Fatalf("Builder.Build should not be called on remote hit, got %d", tb.builtCount)
	}

	expectedFileName := "o-x-1.0.0.tgz"
	if filepath.Base(built1.Path) != expectedFileName {
		t.Fatalf("artifact file name = %q, want %q", filepath.Base(built1.Path), expectedFileName)
	}
	if built1.Artifact.File != expectedFileName {
		t.Fatalf("artifact.File = %q, want %q", built1.Artifact.File, expectedFileName)
	}

	// 2. Later cache lookups via cached() and FindCached() must find it
	cachedBuilt := e.cached(tb, ref, commit, "1.0.0")
	if cachedBuilt == nil {
		t.Fatal("e.cached() returned nil for remote-hit artifact")
	}
	if cachedBuilt.Path != built1.Path {
		t.Fatalf("cachedBuilt.Path = %q, want %q", cachedBuilt.Path, built1.Path)
	}

	findPath := e.FindCached(tb, ref, expectedFileName)
	if findPath == "" {
		t.Fatalf("e.FindCached(%q) returned empty string", expectedFileName)
	}
	if findPath != built1.Path {
		t.Fatalf("FindCached path = %q, want %q", findPath, built1.Path)
	}

	// 3. Second Build for the same version: local cache hit, RemoteCache.Fetch must NOT be called
	e.RemoteCache = &mockRemoteCache{
		fetchFunc: func(ctx context.Context, key, outPath string) (RemoteCacheResult, bool, error) {
			t.Fatal("RemoteCache.Fetch must not be called when artifact is already in local cache")
			return RemoteCacheResult{}, false, nil
		},
	}

	built2, err := e.Build(context.Background(), tb, ref, "@o/x", v)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}
	if tb.builtCount != 0 {
		t.Fatalf("Builder.Build was called %d times on second Build; want 0", tb.builtCount)
	}
	if built2.Path != built1.Path {
		t.Fatalf("second Build returned path %q, want %q", built2.Path, built1.Path)
	}
	if built2.Artifact.SHA512 != announced512 {
		t.Fatalf("second Build SHA512 = %q, want %q", built2.Artifact.SHA512, announced512)
	}
}
