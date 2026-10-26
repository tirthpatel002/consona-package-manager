package procinfo

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

type fakeBackend struct {
	pid, uid, ruid, euid int
	sockErr, envErr      error
	cwdPath              string
	cwdErr               error
	reads                int // environ/cwd/exe calls
}

func (f *fakeBackend) socketPeer(int, int) (int, int, string, error) {
	return f.pid, f.uid, "fake", f.sockErr
}
func (f *fakeBackend) procUIDs(int) (int, int, error) { return f.ruid, f.euid, nil }
func (f *fakeBackend) environ(int) (map[string]string, error) {
	f.reads++
	if f.envErr != nil {
		return nil, f.envErr
	}
	return map[string]string{"NPM_TOKEN": "x"}, nil
}
func (f *fakeBackend) cwd(int) (string, error) { f.reads++; return f.cwdPath, f.cwdErr }
func (f *fakeBackend) exe(int) (string, error) { f.reads++; return "/usr/bin/node", nil }

var (
	srvAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 48731}
	cliAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
)

func me() int { return getuid() }

func TestLookupSameUser(t *testing.T) {
	f := &fakeBackend{pid: 4242, uid: me(), ruid: me(), euid: me(), cwdPath: "/work/app"}
	p, err := lookup(f, srvAddr, cliAddr)
	if err != nil || p.PID != 4242 || p.Env["NPM_TOKEN"] != "x" || p.Cwd != "/work/app" || p.Exe != "/usr/bin/node" || p.CwdErr != nil {
		t.Fatalf("%+v %v", p, err)
	}
}

// A socket or process of another uid is refused before anything of the
// process is read.
func TestLookupRefusesOtherUser(t *testing.T) {
	other := me() + 1
	for name, f := range map[string]*fakeBackend{
		"socket owned by another uid":      {pid: 0, uid: other},
		"process real uid is another":      {pid: 4242, uid: me(), ruid: other, euid: me()},
		"process effective uid is another": {pid: 4242, uid: me(), ruid: me(), euid: other},
	} {
		_, err := lookup(f, srvAddr, cliAddr)
		var oe *OtherUserError
		if !errors.As(err, &oe) || oe.UID != other {
			t.Errorf("%s: err %v", name, err)
		}
		if f.reads != 0 {
			t.Errorf("%s: %d reads of another user's process", name, f.reads)
		}
	}
}

func TestLookupFallbacks(t *testing.T) {
	if _, err := lookup(&fakeBackend{pid: 0, uid: me()}, srvAddr, cliAddr); !errors.Is(err, ErrNotFound) {
		t.Errorf("no pid: %v", err)
	}
	if _, err := lookup(&fakeBackend{pid: getpid(), uid: me(), ruid: me(), euid: me()}, srvAddr, cliAddr); !errors.Is(err, ErrSelf) {
		t.Errorf("self: %v", err)
	}
	if _, err := lookup(&fakeBackend{sockErr: ErrNotFound}, srvAddr, cliAddr); !errors.Is(err, ErrNotFound) {
		t.Errorf("socket error: %v", err)
	}
	if _, err := lookup(nil, srvAddr, cliAddr); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no backend: %v", err)
	}
	f := &fakeBackend{pid: 1}
	if _, err := lookup(f, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}, cliAddr); err == nil || f.reads != 0 {
		t.Errorf("non-loopback must be refused: %v", err)
	}
	if _, err := lookup(&fakeBackend{pid: 4242, uid: me(), ruid: me(), euid: me(), envErr: errors.New("EPERM")}, srvAddr, cliAddr); err == nil {
		t.Error("unreadable environment must be an error (caller falls back)")
	}
	p, err := lookup(&fakeBackend{pid: 4242, uid: me(), ruid: me(), euid: me(), cwdErr: errors.New("EPERM")}, srvAddr, cliAddr)
	if err != nil || p.Cwd != "" || p.CwdErr == nil || p.Env["NPM_TOKEN"] != "x" {
		t.Errorf("unreadable cwd keeps the env: %+v %v", p, err)
	}
}

// pcb builds one xinpcb_n + xsocket_n pair as the kernel lays them out.
func pcb(lport, fport, uid, lastPid, ePid int) []byte {
	in := make([]byte, 104)
	binary.LittleEndian.PutUint32(in, 104)
	binary.LittleEndian.PutUint32(in[4:], xsoInpcb)
	binary.BigEndian.PutUint16(in[16:], uint16(fport))
	binary.BigEndian.PutUint16(in[18:], uint16(lport))
	so := make([]byte, 104)
	binary.LittleEndian.PutUint32(so, 104)
	binary.LittleEndian.PutUint32(so[4:], xsoSocket)
	binary.LittleEndian.PutUint32(so[sockUIDOff:], uint32(uid))
	binary.LittleEndian.PutUint32(so[sockLastPidOff:], uint32(lastPid))
	binary.LittleEndian.PutUint32(so[sockEPidOff:], uint32(ePid))
	buf := make([]byte, 48) // socket buffers etc.: skipped by kind
	binary.LittleEndian.PutUint32(buf, 44)
	binary.LittleEndian.PutUint32(buf[4:], 0x002)
	return append(append(in, so...), buf...)
}

func pcblist(pcbs ...[]byte) []byte {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr, 24)
	b := hdr
	for _, p := range pcbs {
		b = append(b, p...)
	}
	trailer := make([]byte, 24)
	binary.LittleEndian.PutUint32(trailer, 24)
	binary.LittleEndian.PutUint32(trailer[4:], xsoInpcb) // a count that looks like a kind
	return append(b, trailer...)
}

func TestParsePcblist(t *testing.T) {
	const self, uid = 100, 501
	b := pcblist(pcb(48731, 0, uid, self, 0), pcb(1111, 2222, 0, 1, 0),
		pcb(50000, 48731, uid, 777, 0), pcb(48731, 50000, uid, self, 0))
	if r := parsePcblist(b, 50000, 48731, self, uid); !r.found || !r.layoutOK || r.pid != 777 || r.uid != uid {
		t.Fatalf("%+v", r)
	}
	// e_pid when last_pid is unset.
	b = pcblist(pcb(48731, 50001, uid, self, 0), pcb(50001, 48731, 502, 0, 888))
	if r := parsePcblist(b, 50001, 48731, self, uid); !r.found || r.pid != 888 || r.uid != 502 {
		t.Fatalf("e_pid: %+v", r)
	}
	// Our own socket does not show our pid/uid: the layout is not trusted.
	b = pcblist(pcb(50000, 48731, uid, 777, 0), pcb(48731, 50000, uid, 12345, 0))
	if r := parsePcblist(b, 50000, 48731, self, uid); r.found || r.layoutOK {
		t.Fatalf("unrecognised layout must not be used: %+v", r)
	}
	if r := parsePcblist([]byte{1, 2}, 1, 2, self, uid); r.found || r.layoutOK {
		t.Fatalf("garbage: %+v", r)
	}
}

func TestParseProcargs(t *testing.T) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 3)
	b = append(b, "/opt/homebrew/bin/node\x00\x00\x00\x00node\x00\x00/x/npm-cli.js\x00NPM_TOKEN=a=b\x00HOME=/Users/u\x00HOME=dup\x00\x00executable_path=/opt/homebrew/bin/node\x00"...)
	exe, env, err := parseProcargs(b)
	if err != nil || exe != "/opt/homebrew/bin/node" || len(env) != 2 || env["NPM_TOKEN"] != "a=b" || env["HOME"] != "/Users/u" {
		t.Fatalf("%q %v %v", exe, env, err)
	}
	if _, _, err := parseProcargs([]byte{1}); err == nil {
		t.Fatal("short buffer")
	}
}

func TestParseLsof(t *testing.T) {
	out := []byte("p10\nu501\nf3\nn127.0.0.1:48731->127.0.0.1:50000\np20\nu501\nf21\nn127.0.0.1:50000->127.0.0.1:48731\n")
	if pid, uid, ok := parseLsofPeer(out, "127.0.0.1:50000->127.0.0.1:48731"); !ok || pid != 20 || uid != 501 {
		t.Fatalf("%d %d %v", pid, uid, ok)
	}
	if _, _, ok := parseLsofPeer(out, "127.0.0.1:1->127.0.0.1:2"); ok {
		t.Fatal("no match expected")
	}
	if n := parseLsofName([]byte("p1\nfcwd\nn/Users/u/app\n")); n != "/Users/u/app" {
		t.Fatal(n)
	}
}

func TestParseEnv(t *testing.T) {
	env := parseEnv([]byte("A=1\x00B=x=y\x00A=2\x00junk\x00"))
	if len(env) != 2 || env["A"] != "1" || env["B"] != "x=y" {
		t.Fatal(env)
	}
}
