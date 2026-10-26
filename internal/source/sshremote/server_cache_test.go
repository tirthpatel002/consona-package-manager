package sshremote

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
)

func createServerTestTarball(t *testing.T, path, pkgName, version string) []byte {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	content := `{"name":"` + pkgName + `","version":"` + version + `"}`
	hdr := &tar.Header{
		Name: "package/package.json",
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	cacheDir := filepath.Join(home, "cache")
	idx := &localindex.Store{Dir: filepath.Join(home, "cfg")}
	src := local.New(idx, home)
	eng := core.NewEngine(src, cacheDir)
	s := &Server{
		Version: "test-v1",
		Index:   idx,
		Local:   src,
		Engine:  eng,
		Builder: npmpack.Builder{},
	}
	return s, cacheDir
}

func TestServerCacheFetchFound(t *testing.T) {
	s, cacheDir := setupTestServer(t)

	// Seed cache with npm/acme/ui/abc1234567/1.0.0
	entryDir := filepath.Join(cacheDir, "npm", "acme", "ui", "abc1234567", "1.0.0")
	tarballPath := filepath.Join(entryDir, "acme-ui-1.0.0.tgz")
	tarballData := createServerTestTarball(t, tarballPath, "@acme/ui", "1.0.0")
	sum512 := sha512.Sum512(tarballData)
	b64sum := base64.StdEncoding.EncodeToString(sum512[:])

	art := core.Artifact{
		Version:  "1.0.0",
		File:     "acme-ui-1.0.0.tgz",
		Manifest: []byte(`{"name":"@acme/ui","version":"1.0.0"}`),
		SHA512:   b64sum,
	}
	artBytes, _ := json.Marshal(art)
	_ = os.WriteFile(filepath.Join(entryDir, "artifact.json"), artBytes, 0o644)

	var buf bytes.Buffer
	s.handle(context.Background(), Request{Op: "cache_fetch", Key: "npm/acme/ui/abc1234567/1.0.0"}, &buf)

	rd := bufio.NewReader(&buf)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading response line: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal response: %v; line: %s", err, line)
	}
	if !resp.OK {
		t.Fatalf("response not OK: error=%q", resp.Error)
	}
	if resp.Size != int64(len(tarballData)) {
		t.Fatalf("Size = %d, want %d", resp.Size, len(tarballData))
	}
	if resp.SHA512 != b64sum {
		t.Fatalf("SHA512 = %q, want %q", resp.SHA512, b64sum)
	}
	if resp.Package == nil || resp.Package.Name != "@acme/ui" || resp.Package.Version != "1.0.0" {
		t.Fatalf("Package = %+v", resp.Package)
	}

	payload := make([]byte, resp.Size)
	n, err := rd.Read(payload)
	if err != nil && int64(n) != resp.Size {
		t.Fatalf("reading payload: %v (n=%d)", err, n)
	}
	if !bytes.Equal(payload, tarballData) {
		t.Fatal("streamed payload bytes do not match original tarball")
	}
}

func TestServerCacheHasFound(t *testing.T) {
	s, cacheDir := setupTestServer(t)

	entryDir := filepath.Join(cacheDir, "npm", "acme", "ui", "abc1234567", "1.0.0")
	tarballPath := filepath.Join(entryDir, "acme-ui-1.0.0.tgz")
	tarballData := createServerTestTarball(t, tarballPath, "@acme/ui", "1.0.0")
	sum512 := sha512.Sum512(tarballData)
	b64sum := base64.StdEncoding.EncodeToString(sum512[:])

	art := core.Artifact{
		Version:  "1.0.0",
		File:     "acme-ui-1.0.0.tgz",
		Manifest: []byte(`{"name":"@acme/ui"}`),
		SHA512:   b64sum,
	}
	artBytes, _ := json.Marshal(art)
	_ = os.WriteFile(filepath.Join(entryDir, "artifact.json"), artBytes, 0o644)

	var buf bytes.Buffer
	s.handle(context.Background(), Request{Op: "cache_has", Key: "npm/acme/ui/abc1234567/1.0.0"}, &buf)

	var resp Response
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("cache_has not OK: %s", resp.Error)
	}
	if resp.Size != int64(len(tarballData)) {
		t.Fatalf("cache_has Size = %d, want %d", resp.Size, len(tarballData))
	}
	if resp.SHA512 != b64sum {
		t.Fatalf("cache_has SHA512 = %s, want %s", resp.SHA512, b64sum)
	}
}

func TestServerCacheFetchNotFound(t *testing.T) {
	s, _ := setupTestServer(t)

	var buf bytes.Buffer
	s.handle(context.Background(), Request{Op: "cache_fetch", Key: "npm/acme/ui/abc1234567/1.0.0"}, &buf)

	var resp Response
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK || !resp.NotFound {
		t.Fatalf("expected NotFound: true, got %+v", resp)
	}
}

func TestServerCacheFetchInvalidKey(t *testing.T) {
	s, _ := setupTestServer(t)

	var buf bytes.Buffer
	s.handle(context.Background(), Request{Op: "cache_fetch", Key: "../../../etc/passwd"}, &buf)

	var resp Response
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK {
		t.Fatal("expected OK: false for traversal key")
	}
	if resp.NotFound {
		t.Fatal("invalid key should return error, not NotFound: true")
	}
	if !strings.Contains(resp.Error, "invalid cache key") {
		t.Fatalf("expected error mentioning invalid cache key, got %q", resp.Error)
	}
}

func TestServerUnknownOpBackwardsCompatibility(t *testing.T) {
	s, _ := setupTestServer(t)

	var buf bytes.Buffer
	s.handle(context.Background(), Request{Op: "unknown_op"}, &buf)

	var resp Response
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.OK {
		t.Fatal("unknown op should return OK: false")
	}
	if !strings.Contains(resp.Error, "unknown op") {
		t.Fatalf("expected unknown op error, got %q", resp.Error)
	}
}

func TestManagerFetchWithFakeSSH(t *testing.T) {
	e := newEnv(t, "")
	_ = e.m.Store.Add(e.remote, false)

	// Seed remote cache in remote-home/cache
	rhome := filepath.Join(filepath.Dir(e.pkg), "remote-home")
	key := "npm/o/x/1111111111222233333333333333333333333333/1.0.0"
	entryDir := filepath.Join(rhome, "cache", "npm", "o", "x", "1111111111222233333333333333333333333333", "1.0.0")
	tarballPath := filepath.Join(entryDir, "o-x-1.0.0.tgz")
	tarballData := createServerTestTarball(t, tarballPath, "@o/x", "1.0.0")
	sum512 := sha512.Sum512(tarballData)
	b64sum := base64.StdEncoding.EncodeToString(sum512[:])

	art := core.Artifact{
		Version:  "1.0.0",
		File:     "o-x-1.0.0.tgz",
		Manifest: []byte(`{"name":"@o/x","version":"1.0.0"}`),
		SHA512:   b64sum,
	}
	artBytes, _ := json.Marshal(art)
	_ = os.WriteFile(filepath.Join(entryDir, "artifact.json"), artBytes, 0o644)

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "artifact.tgz")
	res, ok, err := e.m.Fetch(context.Background(), key, outPath)
	if err != nil {
		t.Fatalf("m.Fetch unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("m.Fetch expected ok: true, got false")
	}
	if res.RemoteLabel != e.remote.Label() {
		t.Fatalf("RemoteLabel = %q, want %q", res.RemoteLabel, e.remote.Label())
	}
	if res.SHA512 != b64sum {
		t.Fatalf("SHA512 = %q, want %q", res.SHA512, b64sum)
	}
	if gotBytes, err := os.ReadFile(outPath); err != nil || !bytes.Equal(gotBytes, tarballData) {
		t.Fatalf("downloaded file mismatch: err=%v, len=%d", err, len(gotBytes))
	}

	// 2. Offline remote: fake ssh exits 255
	eOffline := newEnv(t, `echo "$*" >> "$CALLS"
echo "ssh: connect to host vm: Connection timed out" >&2
exit 255
`)
	_ = eOffline.m.Store.Add(eOffline.remote, false)

	outOffline := filepath.Join(t.TempDir(), "offline.tgz")
	_, ok, err = eOffline.m.Fetch(context.Background(), key, outOffline)
	if ok {
		t.Fatal("expected ok: false for offline remote")
	}
	if err != nil {
		t.Fatalf("m.Fetch should fail-open (err == nil), got: %v", err)
	}

	// 3. Second call should skip recently failed remote (DownTTL) without invoking ssh again
	initialCalls := eOffline.nCalls()
	_, _, _ = eOffline.m.Fetch(context.Background(), key, outOffline)
	if eOffline.nCalls() != initialCalls {
		t.Fatalf("offline remote was called %d times; want skipped", eOffline.nCalls())
	}
}
