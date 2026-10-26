package core

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
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
