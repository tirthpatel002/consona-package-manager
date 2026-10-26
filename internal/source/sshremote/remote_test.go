package sshremote

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/identity"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/source/local"
)

// The test binary doubles as the remote: a fake `ssh` on PATH re-runs it
// with FAKE_REMOTE_SERVE=1, which serves the remote's local index over
// stdin/stdout exactly like `consona remote-serve` does.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_REMOTE_SERVE") == "1" {
		idx := localindex.Open()
		src := local.New(idx, core.Home())
		s := &Server{Version: "test", Index: idx, Local: src, Engine: core.NewEngine(src, filepath.Join(core.Home(), "cache")), Builder: npmpack.Builder{}}
		if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeFile(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	m       *Manager
	pkg     string
	calls   string // file the fake ssh appends its args to
	bin     string
	remote  Remote
	sshconf string
}

func newEnv(t *testing.T, sshScript string) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{calls: filepath.Join(dir, "calls"), bin: filepath.Join(dir, "bin"), sshconf: filepath.Join(dir, "ssh_config")}
	// Remote side: identity + a registered package.
	rcfg, rhome := filepath.Join(dir, "remote-cfg"), filepath.Join(dir, "remote-home")
	t.Setenv("CONSONA_CONFIG_DIR", rcfg)
	if _, _, err := identity.Init("rem", false); err != nil {
		t.Fatal(err)
	}
	e.pkg = filepath.Join(dir, "remote-pkg")
	writeFile(t, filepath.Join(e.pkg, "package.json"), `{"name":"@rem/tool","version":"2.0.0","main":"index.js"}`, 0o644)
	writeFile(t, filepath.Join(e.pkg, "index.js"), "module.exports = 'v1'\n", 0o644)
	if _, err := (&localindex.Store{Dir: rcfg}).Add(localindex.Entry{Name: "@rem/tool", Path: e.pkg}, false); err != nil {
		t.Fatal(err)
	}
	// Client side: separate config.
	t.Setenv("CONSONA_CONFIG_DIR", filepath.Join(dir, "client-cfg"))
	self, _ := os.Executable()
	if sshScript == "" {
		sshScript = `echo "$*" >> "$CALLS"
CONSONA_CONFIG_DIR="$RCFG" CONSONA_HOME="$RHOME" FAKE_REMOTE_SERVE=1 exec "$SELF"`
	}
	writeFile(t, filepath.Join(e.bin, "ssh"), "#!/bin/sh\n"+sshScript+"\n", 0o755)
	t.Setenv("PATH", e.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", e.calls)
	t.Setenv("RCFG", rcfg)
	t.Setenv("RHOME", rhome)
	t.Setenv("SELF", self)
	t.Setenv("CONSONA_SSH_CONFIG", e.sshconf)
	t.Setenv("CONSONA_REMOTE_CMD", "")
	t.Setenv("CONSONA_SSH_AUTODETECT", "")
	home := filepath.Join(dir, "client-home")
	e.m = NewManager(home)
	e.m.Client.ControlDir = ""
	e.remote = Remote{Name: "vm", Target: "ubuntu@vm.example", Port: 2222}
	return e
}

func (e *env) nCalls() int {
	b, _ := os.ReadFile(e.calls)
	return strings.Count(string(b), "\n")
}

func TestRemoteInstallFlowVerifiesIntegrity(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	e := newEnv(t, "")
	if err := e.m.Store.Add(e.remote, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, st := e.m.Find(ctx, "@rem/tool")
	if r == nil || r.Name != "vm" || st.Username != "rem" || st.Device == nil {
		t.Fatalf("find: %+v %+v", r, st)
	}
	b, _ := os.ReadFile(e.calls)
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=3", "-p 2222", "-- ubuntu@vm.example"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("ssh args %q lack %q", b, want)
		}
	}
	if strings.Contains(string(b), "StrictHostKeyChecking") {
		t.Error("host key checking must never be changed")
	}
	ref := core.Ref{Owner: "rem", Name: "tool"}
	revs, err := e.m.Revisions(ctx, *r, ref)
	if err != nil || len(revs) != 1 || !strings.HasPrefix(revs[0].Version, "2.0.0-local.1.h") || !strings.HasPrefix(revs[0].Commit, CommitPrefix+r.Key()+"-") {
		t.Fatalf("revisions %v %+v", err, revs)
	}
	info, ok, err := e.m.PrebuiltInfo(ctx, ref, revs[0])
	if !ok || err != nil || info.Version != revs[0].Version {
		t.Fatalf("info %v %v %+v", ok, err, info)
	}
	out := t.TempDir()
	art, ok, err := e.m.FetchPrebuilt(ctx, ref, revs[0], out)
	if !ok || err != nil {
		t.Fatalf("fetch %v %v", ok, err)
	}
	data, err := os.ReadFile(filepath.Join(out, art.File))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(data)
	if base64.StdEncoding.EncodeToString(sum[:]) != art.SHA512 || art.File != npmpack.FileName("@rem/tool", revs[0].Version) {
		t.Fatal("artifact integrity")
	}
	// Remote content changes: resolve gives a new version; fetching the
	// old one still works from the remote's cache.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, filepath.Join(e.pkg, "index.js"), "module.exports = 'v2'\n", 0o644)
	revs2, err := e.m.Revisions(ctx, *r, ref)
	if err != nil || revs2[0].Version == revs[0].Version || revs2[0].Commit == revs[0].Commit {
		t.Fatalf("after change %v %+v", err, revs2)
	}
	if _, _, err := e.m.FetchPrebuilt(ctx, ref, revs[0], t.TempDir()); err != nil {
		t.Fatalf("old version fetch: %v", err)
	}
	// Only registered names are served.
	if _, err := e.m.Revisions(ctx, *r, core.Ref{Owner: "rem", Name: "secret"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unregistered name: %v", err)
	}
	// Tampered integrity is rejected.
	e.m.mu.Lock()
	p := *e.m.pkgs[revs2[0].Commit]
	p.SHA512 = base64.StdEncoding.EncodeToString(make([]byte, 64))
	e.m.pkgs[revs2[0].Commit] = &p
	e.m.mu.Unlock()
	if _, _, err := e.m.FetchPrebuilt(ctx, ref, revs2[0], t.TempDir()); err == nil {
		t.Fatal("tampered sha512 accepted")
	}
}

func TestListCacheAvoidsSSHForUnknownNames(t *testing.T) {
	e := newEnv(t, `echo "$*" >> "$CALLS"
read line
echo '{"protocol":"consona-remote/1","ok":true,"username":"rem","packages":[{"name":"@rem/a"}]}'`)
	e.m.Store.Add(e.remote, false)
	ctx := context.Background()
	if r, _ := e.m.Find(ctx, "@other/x"); r != nil {
		t.Fatal("found unknown name")
	}
	if r, _ := e.m.Find(ctx, "@rem/a"); r == nil {
		t.Fatal("not found")
	}
	if r, _ := e.m.Find(ctx, "@other/y"); r != nil {
		t.Fatal("found")
	}
	if n := e.nCalls(); n != 1 {
		t.Fatalf("%d ssh calls, want 1 (list cached)", n)
	}
	if !e.m.Scopes()["rem"] {
		t.Fatal("scopes")
	}
	// State survives a new manager (daemon restart).
	m2 := NewManager(filepath.Dir(e.m.StatePath))
	m2.Client.ControlDir = ""
	if r, _ := m2.Find(ctx, "@rem/a"); r == nil || e.nCalls() != 1 {
		t.Fatal("persisted state not used")
	}
}

func TestUnreachableAndNoConsona(t *testing.T) {
	for _, c := range []struct {
		script, want string
		noConsona    bool
	}{
		{`echo "$*" >> "$CALLS"; echo "ssh: connect to host vm port 22: Connection timed out" >&2; exit 255`, "Connection timed out", false},
		{`echo "$*" >> "$CALLS"; echo "Host key verification failed." >&2; exit 255`, "host key", false},
		{`echo "$*" >> "$CALLS"; echo "ubuntu@vm: Permission denied (publickey)." >&2; exit 255`, "never asks for passwords", false},
		{`echo "$*" >> "$CALLS"; echo consona-not-found >&2; exit 127`, "not installed on the remote", true},
		{`echo "$*" >> "$CALLS"; echo "Welcome to my server"`, "unexpected output", false},
	} {
		e := newEnv(t, c.script)
		e.m.Store.Add(e.remote, false)
		st := e.m.Probe(context.Background(), e.remote)
		if st.OK || !strings.Contains(st.Err, c.want) || st.NoConsona != c.noConsona {
			t.Errorf("%q: %+v", c.want, st)
		}
		// A down remote is not retried for every name.
		e.m.Find(context.Background(), "@a/b")
		e.m.Find(context.Background(), "@a/c")
		if n := e.nCalls(); n != 1 {
			t.Errorf("%q: %d ssh calls", c.want, n)
		}
	}
}

func TestAutoDetectedHosts(t *testing.T) {
	e := newEnv(t, `echo "$*" >> "$CALLS"
read line
echo '{"protocol":"consona-remote/1","ok":true,"packages":[{"name":"@mac/p"}]}'`)
	os.WriteFile(e.sshconf, []byte("Host mac\n  HostName mac.local\nHost vm\n  HostName 1.2.3.4\nHost *.corp\n  User x\n"), 0o600)
	e.m.Store.Add(Remote{Name: "vm", Target: "ubuntu@1.2.3.4"}, false)
	rs := e.m.Remotes()
	if len(rs) != 2 || rs[0].Name != "vm" || rs[0].Auto || rs[1].Name != "mac" || !rs[1].Auto {
		t.Fatalf("%+v", rs)
	}
	if r, _ := e.m.Find(context.Background(), "@mac/p"); r == nil || r.Name != "vm" {
		t.Fatalf("explicit remote first: %+v", r)
	}
	b, _ := os.ReadFile(e.calls)
	if !strings.Contains(string(b), "-F "+e.sshconf) {
		t.Fatal("ssh config not passed")
	}
	t.Setenv("CONSONA_SSH_AUTODETECT", "0")
	if len(NewManager(t.TempDir()).Remotes()) != 1 {
		t.Fatal("autodetect not disabled")
	}
}

func TestRemoteValidation(t *testing.T) {
	for _, r := range []Remote{{Name: "x", Target: "-oProxyCommand=evil"}, {Name: "-x", Target: "h"}, {Name: "x", Target: "a b"}} {
		if r.Validate() == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	if (Remote{Name: "vm", Target: "ubuntu@203.0.113.5"}).Validate() != nil {
		t.Fatal("valid remote rejected")
	}
}
