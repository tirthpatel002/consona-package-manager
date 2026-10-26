package chain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
	"github.com/tirthpatel002/consona-package-manager/internal/source/sshremote"
)

type fakeGH struct {
	has   map[string]bool
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeGH) Describe(r core.Ref) string { return "github.com/" + r.String() }
func (f *fakeGH) Revisions(ctx context.Context, r core.Ref) ([]core.Revision, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.has[r.String()] {
		return []core.Revision{{Version: "1.0.0", Commit: strings.Repeat("a", 40), Tag: "v1.0.0"}}, nil
	}
	return nil, core.ErrNotFound
}
func (f *fakeGH) Fetch(context.Context, core.Ref, string, string) error { return nil }

type sink []Resolution

func (s *sink) Resolved(r Resolution) { *s = append(*s, r) }

func newChain(t *testing.T, sshScript string) (*Chain, *fakeGH, *sink, string) {
	t.Helper()
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	os.MkdirAll(pkg, 0o755)
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@acme/lib","version":"1.0.0"}`), 0o644)
	idx := &localindex.Store{Dir: filepath.Join(dir, "cfg")}
	if _, err := idx.Add(localindex.Entry{Name: "@acme/lib", Path: pkg}, false); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho x >> \""+filepath.Join(dir, "calls")+"\"\n"+sshScript+"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONSONA_SSH_CONFIG", filepath.Join(dir, "none"))
	t.Setenv("CONSONA_REMOTE_CMD", "")
	m := sshremote.NewManager(filepath.Join(dir, "home"))
	m.Client.ControlDir = ""
	m.Store = &sshremote.Store{Dir: filepath.Join(dir, "cfg")}
	if sshScript != "" {
		m.Store.Add(sshremote.Remote{Name: "vm", Target: "u@vm"}, false)
	}
	gh := &fakeGH{has: map[string]bool{"acme/lib": true, "acme/gh-only": true, "rem/tool": true}}
	s := &sink{}
	return &Chain{Local: local.New(idx, filepath.Join(dir, "home")), Remotes: m, GitHub: gh, Sink: s, ThisDevice: "dev1"}, gh, s, dir
}

const remoteScript = `read line
case "$line" in
*'"op":"list"'*) echo '{"protocol":"consona-remote/1","ok":true,"device":{"id":"r1","hostname":"vm"},"packages":[{"name":"@rem/tool"},{"name":"@acme/lib"}]}';;
*'"op":"resolve"'*) echo '{"protocol":"consona-remote/1","ok":true,"package":{"name":"@rem/tool","version":"3.0.0-local.1.habcdef12","hash":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890","sha512":"x","size":1}}';;
esac`

func TestLocalWinsOverRemoteAndGitHub(t *testing.T) {
	c, _, s, _ := newChain(t, remoteScript)
	revs, err := c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "lib"})
	if err != nil || !strings.HasPrefix(revs[0].Commit, local.CommitPrefix) || !strings.HasPrefix(revs[0].Version, "1.0.0-local.1.h") {
		t.Fatalf("%v %+v", err, revs)
	}
	if len(*s) != 1 || (*s)[0].Kind != "local" || (*s)[0].Device != "dev1" {
		t.Fatalf("%+v", *s)
	}
	if !c.Live(core.Ref{Owner: "acme", Name: "lib"}) || c.Live(core.Ref{Owner: "rem", Name: "tool"}) {
		t.Fatal("live")
	}
}

func TestRemoteBeforeGitHub(t *testing.T) {
	c, _, s, _ := newChain(t, remoteScript)
	revs, err := c.Revisions(context.Background(), core.Ref{Owner: "rem", Name: "tool"})
	if err != nil || !strings.HasPrefix(revs[0].Commit, sshremote.CommitPrefix) {
		t.Fatalf("%v %+v", err, revs)
	}
	if (*s)[0].Kind != "ssh" || (*s)[0].Device != "vm (r1)" {
		t.Fatalf("%+v", *s)
	}
	if got := c.ResolutionsSince(time.Time{}); len(got) != 1 || !strings.Contains(got[0], "@rem/tool@3.0.0-local.1.habcdef12 <- ssh vm") {
		t.Fatalf("%q", got)
	}
	if !c.PrivateScopes()["rem"] || !c.PrivateScopes()["acme"] {
		t.Fatal("scopes")
	}
}

func TestGitHubFallbackAndNotFound(t *testing.T) {
	c, gh, s, _ := newChain(t, remoteScript)
	gh.delay = 50 * time.Millisecond
	revs, err := c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "gh-only"})
	if err != nil || revs[0].Version != "1.0.0" || (*s)[0].Kind != "github" {
		t.Fatalf("%v %+v %+v", err, revs, *s)
	}
	_, err = c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "nope"})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	c.GitHub = nil
	_, err = c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "nope"})
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), "GitHub not available") {
		t.Fatal(err)
	}
}

func TestDownRemoteDoesNotBlockGitHub(t *testing.T) {
	c, _, s, _ := newChain(t, `sleep 0.2; echo "Connection refused" >&2; exit 255`)
	start := time.Now()
	if _, err := c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "gh-only"}); err != nil || (*s)[0].Kind != "github" {
		t.Fatal(err)
	}
	// Second lookup: the remote is known down, no ssh wait at all.
	t2 := time.Now()
	if _, err := c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "gh-only"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t2); d > 150*time.Millisecond {
		t.Fatalf("second lookup took %v", d)
	}
	_ = start
}

func TestNoRemotesNoSSH(t *testing.T) {
	c, _, _, dir := newChain(t, "")
	c.Revisions(context.Background(), core.Ref{Owner: "acme", Name: "gh-only"})
	if _, err := os.Stat(filepath.Join(dir, "calls")); err == nil {
		t.Fatal("ssh ran with no remotes configured")
	}
}

func TestDescribeNotFound(t *testing.T) {
	c, _, _, _ := newChain(t, "exit 255")
	ref := core.Ref{Owner: "acme", Name: "missing"}
	if got := c.Describe(ref); got != "github.com/acme/missing (also not in this device's registered folders, 1 ssh remote(s))" {
		t.Fatalf("Describe = %q", got)
	}
	c.GitHub, c.GitHubError = nil, errors.New("no login")
	if got := strings.Join(c.Places(ref), ", "); got != "this device's registered folders, 1 ssh remote(s), GitHub (not available: no login)" {
		t.Fatalf("Places = %q", got)
	}
}
