package procinfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

var sys backend = darwinBackend{}

type darwinBackend struct{}

// proc_info(2): the system call behind libproc's proc_pidinfo. Calling it
// through syscall.Syscall6 goes via libSystem, so no cgo is needed.
const (
	sysProcInfo          = 336 // SYS_PROC_INFO
	procInfoCallPidinfo  = 2   // PROC_INFO_CALL_PIDINFO
	procPidVnodePathInfo = 9   // PROC_PIDVNODEPATHINFO: struct proc_vnodepathinfo
	procPidPathInfo      = 11  // PROC_PIDPATHINFO
	vnodePathInfoSize    = 2352
	cdirPathOff          = 152 // pvi_cdir.vip_path, after struct vnode_info
	maxPath              = 1024
)

func procPidinfo(pid, flavor int, buf []byte) (int, error) {
	r, _, e := syscall.Syscall6(sysProcInfo, procInfoCallPidinfo, uintptr(pid), uintptr(flavor), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}

func (darwinBackend) socketPeer(cport, sport int) (int, int, string, error) {
	b, err := unix.SysctlRaw("net.inet.tcp.pcblist_n")
	if err == nil {
		r := parsePcblist(b, cport, sport, getpid(), getuid())
		if r.layoutOK {
			if !r.found {
				return 0, 0, "", fmt.Errorf("%w (no net.inet.tcp.pcblist_n entry for port %d -> %d)", ErrNotFound, cport, sport)
			}
			if r.uid != getuid() {
				return 0, r.uid, "pcblist", nil
			}
			return r.pid, r.uid, "pcblist", nil
		}
		err = errors.New("record layout not recognised")
	}
	pid, uid, lerr := lsofPeer(cport, sport)
	if lerr != nil {
		return 0, 0, "", fmt.Errorf("net.inet.tcp.pcblist_n: %v; lsof: %w", err, lerr)
	}
	if uid != getuid() {
		return 0, uid, "lsof", nil
	}
	return pid, uid, "lsof", nil
}

func lsofPath() (string, error) {
	if _, err := os.Stat("/usr/sbin/lsof"); err == nil {
		return "/usr/sbin/lsof", nil
	}
	return exec.LookPath("lsof")
}

func runLsof(args ...string) ([]byte, error) {
	p, err := lsofPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("lsof timed out")
	}
	if err != nil && len(out) == 0 { // lsof exits 1 when nothing matches
		return nil, fmt.Errorf("%w (lsof: %v)", ErrNotFound, err)
	}
	return out, nil
}

// lsofPeer: as a non-root user lsof only sees this user's processes.
func lsofPeer(cport, sport int) (int, int, error) {
	out, err := runLsof("-nP", "-w", "-iTCP@127.0.0.1:"+strconv.Itoa(cport), "-sTCP:ESTABLISHED", "-F", "pun")
	if err != nil {
		return 0, 0, err
	}
	pid, uid, ok := parseLsofPeer(out, fmt.Sprintf("127.0.0.1:%d->127.0.0.1:%d", cport, sport))
	if !ok {
		return 0, 0, fmt.Errorf("%w (lsof lists no socket 127.0.0.1:%d->127.0.0.1:%d)", ErrNotFound, cport, sport)
	}
	return pid, uid, nil
}

func (darwinBackend) procUIDs(pid int) (int, int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, 0, fmt.Errorf("no process %d", pid)
	}
	return int(kp.Eproc.Pcred.P_ruid), int(kp.Eproc.Ucred.Uid), nil
}

// environ reads kern.procargs2 (allowed for this user's processes, except
// ones the system protects).
func (darwinBackend) environ(pid int) (map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, fmt.Errorf("kern.procargs2: %w", err)
	}
	_, env, err := parseProcargs(b)
	return env, err
}

func (darwinBackend) cwd(pid int) (string, error) {
	buf := make([]byte, vnodePathInfoSize)
	n, err := procPidinfo(pid, procPidVnodePathInfo, buf)
	if err == nil && n >= cdirPathOff+1 {
		p := buf[cdirPathOff : cdirPathOff+maxPath]
		if i := bytes.IndexByte(p, 0); i >= 0 {
			p = p[:i]
		}
		if len(p) > 0 && p[0] == '/' {
			return string(p), nil
		}
		err = errors.New("no path in PROC_PIDVNODEPATHINFO")
	} else if err == nil {
		err = fmt.Errorf("short PROC_PIDVNODEPATHINFO answer (%d bytes)", n)
	}
	out, lerr := runLsof("-a", "-w", "-p", strconv.Itoa(pid), "-d", "cwd", "-F", "n")
	if lerr == nil {
		if p := parseLsofName(out); len(p) > 0 && p[0] == '/' {
			return p, nil
		}
		lerr = errors.New("no cwd entry")
	}
	return "", fmt.Errorf("proc_pidinfo: %v; lsof: %v", err, lerr)
}

func (darwinBackend) exe(pid int) (string, error) {
	buf := make([]byte, 4*maxPath) // PROC_PIDPATHINFO_MAXSIZE
	if _, err := procPidinfo(pid, procPidPathInfo, buf); err != nil {
		return "", err
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	if len(buf) == 0 {
		return "", errors.New("empty executable path")
	}
	return string(buf), nil
}
