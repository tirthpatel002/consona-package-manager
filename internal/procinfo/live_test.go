package procinfo

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain doubles as the peer process for TestPeerLive: it connects to
// PROCINFO_LIVE_ADDR and waits for stdin to close.
func TestMain(m *testing.M) {
	if a := os.Getenv("PROCINFO_LIVE_ADDR"); a != "" {
		c, err := net.Dial("tcp", a)
		if err != nil {
			os.Exit(3)
		}
		c.Write([]byte("x"))
		b := make([]byte, 1)
		os.Stdin.Read(b)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startPeer runs a child process (this test binary) that connects to a
// listener and stays connected; it returns our end of the connection.
func startPeer(t *testing.T) (net.Conn, *exec.Cmd, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PROCINFO_LIVE_ADDR="+ln.Addr().String(), "PROCINFO_LIVE_MARK=peer-env-ok")
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Wait() })
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.Read(make([]byte, 1))
	want, _ := filepath.EvalSymlinks(dir)
	return c, cmd, want
}

// The real backend finds a child process (same user) on the other end of
// a loopback connection and reads its env, cwd and executable.
func TestPeerLive(t *testing.T) {
	if sys == nil {
		if _, err := Peer(srvAddr, cliAddr); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		t.Skip("no backend on this OS")
	}
	c, cmd, want := startPeer(t)
	start := time.Now()
	p, err := Peer(c.LocalAddr(), c.RemoteAddr())
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(p.Cwd)
	if p.PID != cmd.Process.Pid || p.UID != os.Getuid() || p.Env["PROCINFO_LIVE_MARK"] != "peer-env-ok" || got != want || p.Exe == "" {
		t.Fatalf("pid %d (want %d) uid %d env-mark %q cwd %q (want %q) cwdErr %v exe %q", p.PID, cmd.Process.Pid, p.UID,
			p.Env["PROCINFO_LIVE_MARK"], p.Cwd, want, p.CwdErr, p.Exe)
	}
	t.Logf("peer pid %d via %s, exe %s, %d env vars, lookup took %v", p.PID, p.Via, p.Exe, len(p.Env), took)
	// Our own end of the same connection is this process.
	if _, err := Peer(c.RemoteAddr(), c.LocalAddr()); !errors.Is(err, ErrSelf) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse lookup: %v", err)
	}
}
