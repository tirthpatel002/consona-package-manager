package procinfo

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The lsof fallbacks (used if pcblist_n or proc_pidinfo do not answer)
// find the same process and working directory.
func TestLsofFallbackLive(t *testing.T) {
	c, cmd, want := startPeer(t)
	l, r := c.LocalAddr().(*net.TCPAddr), c.RemoteAddr().(*net.TCPAddr)
	start := time.Now()
	pid, uid, err := lsofPeer(r.Port, l.Port)
	took := time.Since(start)
	if err != nil || pid != cmd.Process.Pid || uid != os.Getuid() {
		t.Fatalf("lsofPeer: %d %d %v (want pid %d)", pid, uid, err, cmd.Process.Pid)
	}
	out, err := runLsof("-a", "-w", "-p", strconv.Itoa(pid), "-d", "cwd", "-F", "n")
	got, _ := filepath.EvalSymlinks(parseLsofName(out))
	if err != nil || got != want {
		t.Fatalf("lsof cwd: %q %v (want %q)", got, err, want)
	}
	t.Logf("lsof peer lookup took %v", took)
	// A process of another user (launchd, pid 1) is never read: the uid
	// check refuses it, and the system would not answer anyway.
	if _, _, err := (darwinBackend{}).procUIDs(1); err != nil {
		t.Fatalf("uids of pid 1: %v", err)
	}
	if ruid, euid, _ := (darwinBackend{}).procUIDs(1); ruid == os.Getuid() || euid == os.Getuid() {
		t.Skip("running as root")
	}
	if _, err := (darwinBackend{}).environ(1); err == nil {
		t.Fatal("kern.procargs2 of another user's process unexpectedly readable")
	}
}
