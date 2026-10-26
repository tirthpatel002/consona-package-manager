// Package procinfo identifies the local process on the other end of a
// loopback TCP connection and reads what npm's config depends on: its
// environment, working directory and executable.
//
// Only processes of the current user are read. The owner of the socket is
// checked first, then the real and effective uid of the process; a process
// of another user is refused before any of its data is touched.
//
// Backends (no cgo anywhere, so static release builds keep working):
//   - Linux: /proc/net/tcp{,6} for the socket owner, /proc/<pid>/fd for the
//     process holding it, /proc/<pid>/{status,environ,cwd,exe}.
//   - macOS: the net.inet.tcp.pcblist_n sysctl for the socket owner (lsof
//     as a fallback), kern.proc.pid for the uids, kern.procargs2 for the
//     environment, proc_pidinfo (PROC_PIDVNODEPATHINFO, PROC_PIDPATHINFO)
//     through the proc_info system call for the working directory and
//     executable (lsof as a fallback for the working directory).
//   - Elsewhere: ErrUnsupported (a Windows backend can implement backend).
package procinfo

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
)

// Process is what the peer process exposes.
type Process struct {
	PID    int
	UID    int
	Env    map[string]string // the process's environment (never nil)
	Cwd    string            // working directory; "" if unreadable (CwdErr says why)
	CwdErr error
	Exe    string // executable path; "" if unreadable
	Via    string // how the socket's owner was found, e.g. "proc", "pcblist", "lsof"
}

var (
	// ErrUnsupported: this OS has no backend.
	ErrUnsupported = errors.New("peer process lookup is not implemented on " + runtime.GOOS)
	// ErrNotFound: no process of this user holds the client socket (it
	// closed, or the system does not show it).
	ErrNotFound = errors.New("no local process found for the connection")
	// ErrSelf: the connection comes from this process.
	ErrSelf = errors.New("the connection comes from this process")
)

// OtherUserError means the peer belongs to another user: nothing of it is read.
type OtherUserError struct{ UID int }

func (e *OtherUserError) Error() string {
	return fmt.Sprintf("the connection belongs to another user (uid %d); its process is not read", e.UID)
}

// backend is one OS's way to find and read a process.
type backend interface {
	// socketPeer returns the uid owning the TCP socket with local port
	// cport and remote port sport, and (only if that uid is the current
	// user's) the pid holding it (0 = not found).
	socketPeer(cport, sport int) (pid, uid int, via string, err error)
	// procUIDs returns the real and effective uid of pid.
	procUIDs(pid int) (ruid, euid int, err error)
	environ(pid int) (map[string]string, error)
	cwd(pid int) (string, error)
	exe(pid int) (string, error)
}

var (
	getuid = os.Getuid
	getpid = os.Getpid
)

// Peer finds and reads the process on the other end of a loopback TCP
// connection; local and remote are the server side's addresses.
func Peer(local, remote net.Addr) (*Process, error) { return lookup(sys, local, remote) }

func lookup(b backend, local, remote net.Addr) (*Process, error) {
	if b == nil {
		return nil, ErrUnsupported
	}
	l, ok1 := local.(*net.TCPAddr)
	r, ok2 := remote.(*net.TCPAddr)
	if !ok1 || !ok2 || l == nil || r == nil {
		return nil, errors.New("not a TCP connection")
	}
	if !l.IP.IsLoopback() || !r.IP.IsLoopback() {
		return nil, errors.New("not a loopback connection")
	}
	pid, uid, via, err := b.socketPeer(r.Port, l.Port)
	if err != nil {
		return nil, err
	}
	me := getuid()
	if uid != me {
		return nil, &OtherUserError{UID: uid}
	}
	if pid <= 0 {
		return nil, ErrNotFound
	}
	if pid == getpid() {
		return nil, ErrSelf
	}
	// The socket is ours; the process holding it must be too (it could
	// have been passed to, or the pid reused by, another user's process).
	ruid, euid, err := b.procUIDs(pid)
	if err != nil {
		return nil, fmt.Errorf("pid %d (found via %s): uids: %w", pid, via, err)
	}
	if ruid != me {
		return nil, &OtherUserError{UID: ruid}
	}
	if euid != me {
		return nil, &OtherUserError{UID: euid}
	}
	env, err := b.environ(pid)
	if err != nil {
		return nil, fmt.Errorf("pid %d (found via %s): environment: %w", pid, via, err)
	}
	if env == nil {
		env = map[string]string{}
	}
	p := &Process{PID: pid, UID: uid, Env: env, Via: via}
	p.Cwd, p.CwdErr = b.cwd(pid)
	if p.Cwd == "" && p.CwdErr == nil {
		p.CwdErr = errors.New("empty working directory")
	}
	p.Exe, _ = b.exe(pid)
	return p, nil
}

// parseEnv splits NUL-separated KEY=value strings (the first value of a
// repeated key wins, as getenv does).
func parseEnv(b []byte) map[string]string {
	env := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if k, v, ok := bytes.Cut(kv, []byte("=")); ok && len(k) > 0 {
			if _, dup := env[string(k)]; !dup {
				env[string(k)] = string(v)
			}
		}
	}
	return env
}
