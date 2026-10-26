package npm

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// peerClient finds the process on the other end of r's TCP connection via
// /proc (Linux), and reads its environment and working directory.
func peerClient(r *http.Request) (c *client, sameUser, found bool) {
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	ra, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if la == nil || err != nil {
		return nil, false, false
	}
	lt, ok := la.(*net.TCPAddr)
	if !ok {
		return nil, false, false
	}
	key := r.RemoteAddr
	if v, ok := peerCache.Load(key); ok {
		e := v.(peerEntry)
		if time.Since(e.at) < 10*time.Second {
			return e.c, e.same, e.c != nil || !e.same
		}
	}
	uid, inode, ok := socketOwner(ra.Port, lt.Port)
	if !ok {
		return nil, false, false
	}
	if uid != os.Getuid() {
		peerCache.Store(key, peerEntry{at: time.Now()})
		return nil, false, true
	}
	pid := socketPid(inode)
	if pid == "" || pid == strconv.Itoa(os.Getpid()) { // unknown, or ourselves
		return nil, true, false
	}
	c = &client{env: map[string]string{}}
	if b, err := os.ReadFile("/proc/" + pid + "/environ"); err == nil {
		for _, kv := range strings.Split(string(b), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				c.env[k] = v
			}
		}
	}
	c.cwd, _ = os.Readlink("/proc/" + pid + "/cwd")
	c.node, _ = os.Readlink("/proc/" + pid + "/exe")
	peerCache.Store(key, peerEntry{c: c, same: true, at: time.Now()})
	return c, true, true
}

type peerEntry struct {
	c    *client
	same bool
	at   time.Time
}

var peerCache sync.Map

// socketOwner finds the uid and inode of the client socket 127.0.0.1:cport
// connected to our port.
func socketOwner(cport, sport int) (uid int, inode string, ok bool) {
	want := fmt.Sprintf(":%04X", cport)
	peer := fmt.Sprintf(":%04X", sport)
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			x := strings.Fields(sc.Text())
			if len(x) < 10 || !strings.HasSuffix(x[1], want) || !strings.HasSuffix(x[2], peer) {
				continue
			}
			u, err := strconv.Atoi(x[7])
			fh.Close()
			return u, x[9], err == nil
		}
		fh.Close()
	}
	return 0, "", false
}

// socketPid finds the process of ours holding socket inode.
func socketPid(inode string) string {
	target := "socket:[" + inode + "]"
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range procs {
		fds, err := os.ReadDir(p + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if l, _ := os.Readlink(p + "/fd/" + fd.Name()); l == target {
				return filepath.Base(p)
			}
		}
	}
	return ""
}
