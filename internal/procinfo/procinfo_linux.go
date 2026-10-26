package procinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var sys backend = linuxBackend{}

type linuxBackend struct{}

// socketPeer reads /proc/net/tcp{,6} for the socket 127.0.0.1:cport ->
// :sport, then looks for the inode among this user's processes' fds.
func (linuxBackend) socketPeer(cport, sport int) (int, int, string, error) {
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
			fh.Close()
			uid, err := strconv.Atoi(x[7])
			if err != nil {
				return 0, 0, "", fmt.Errorf("%s: bad uid %q", f, x[7])
			}
			if uid != getuid() {
				return 0, uid, "proc", nil
			}
			return socketPid(x[9]), uid, "proc", nil
		}
		fh.Close()
	}
	return 0, 0, "", fmt.Errorf("%w (no /proc/net/tcp entry for port %d -> %d)", ErrNotFound, cport, sport)
}

// socketPid finds the process holding socket inode (only this user's
// processes' fds are readable).
func socketPid(inode string) int {
	target := "socket:[" + inode + "]"
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range procs {
		fds, err := os.ReadDir(p + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if l, _ := os.Readlink(p + "/fd/" + fd.Name()); l == target {
				pid, _ := strconv.Atoi(filepath.Base(p))
				return pid
			}
		}
	}
	return 0
}

func (linuxBackend) procUIDs(pid int) (int, int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "Uid:" {
			r, err1 := strconv.Atoi(f[1])
			e, err2 := strconv.Atoi(f[2])
			if err1 == nil && err2 == nil {
				return r, e, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("no Uid line in /proc/%d/status", pid)
}

func (linuxBackend) environ(pid int) (map[string]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	return parseEnv(b), nil
}

func (linuxBackend) cwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
func (linuxBackend) exe(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
}
