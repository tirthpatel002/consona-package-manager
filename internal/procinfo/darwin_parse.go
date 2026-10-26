package procinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// The macOS parsers are plain byte parsers, built on every OS so their
// tests run everywhere.

// net.inet.tcp.pcblist_n is a list of self-describing records (each starts
// with a uint32 length and a uint32 kind, padded to 8 bytes), after an
// xinpgen header: per connection an xinpcb_n (ports), then an xsocket_n
// (owner uid and pid), socket buffers and an xtcpcb_n.
const (
	xsoSocket = 0x001 // XSO_SOCKET
	xsoInpcb  = 0x010 // XSO_INPCB
	// xinpcb_n: inp_fport at 16, inp_lport at 18 (network byte order).
	inpcbMinLen = 64
	// xsocket_n: so_uid at 64, so_last_pid at 68, so_e_pid at 72. These
	// offsets are checked at run time against this process's own socket
	// for the connection before the peer's values are trusted.
	sockUIDOff, sockLastPidOff, sockEPidOff = 64, 68, 72
	sockMinLen                              = 76
)

type pcbPeer struct {
	pid, uid int
	found    bool // the client socket (lport cport, fport sport) was listed
	layoutOK bool // the server socket showed this process's pid and uid
}

// parsePcblist finds the owner of the client socket (local port cport,
// remote port sport) and checks the layout with our own server socket.
func parsePcblist(b []byte, cport, sport, selfPid, selfUID int) pcbPeer {
	var r pcbPeer
	if len(b) < 8 {
		return r
	}
	off := align8(int(binary.LittleEndian.Uint32(b)))
	var lport, fport int
	serverSeen := false
	for off+8 <= len(b) {
		l := int(binary.LittleEndian.Uint32(b[off:]))
		kind := binary.LittleEndian.Uint32(b[off+4:])
		if l < 8 || off+l > len(b) {
			break
		}
		rec := b[off : off+l]
		switch {
		case kind == xsoInpcb && l >= inpcbMinLen:
			fport = int(binary.BigEndian.Uint16(rec[16:]))
			lport = int(binary.BigEndian.Uint16(rec[18:]))
		case kind == xsoSocket && l >= sockMinLen:
			uid := int(binary.LittleEndian.Uint32(rec[sockUIDOff:]))
			pid := int(int32(binary.LittleEndian.Uint32(rec[sockLastPidOff:])))
			if pid <= 0 {
				pid = int(int32(binary.LittleEndian.Uint32(rec[sockEPidOff:])))
			}
			switch {
			case lport == cport && fport == sport && !r.found:
				r.pid, r.uid, r.found = pid, uid, true
			case lport == sport && fport == cport && !serverSeen:
				serverSeen = true
				r.layoutOK = pid == selfPid && uid == selfUID
			}
			lport, fport = -1, -1 // one socket per pcb
		}
		off += align8(l)
	}
	if !r.layoutOK {
		r.found = false
	}
	return r
}

func align8(n int) int { return (n + 7) &^ 7 }

// parseProcargs reads a kern.procargs2 buffer: int32 argc, the exec path,
// NUL padding, argc NUL-terminated arguments (or a process title over
// them, NUL-padded), then the environment
// (NUL-terminated strings up to an empty one; Apple's own strings follow).
func parseProcargs(b []byte) (exe string, env map[string]string, err error) {
	if len(b) < 4 {
		return "", nil, errors.New("short kern.procargs2 buffer")
	}
	argc := int(int32(binary.LittleEndian.Uint32(b)))
	p := 4
	next := func() (string, bool) {
		if p >= len(b) {
			return "", false
		}
		i := bytes.IndexByte(b[p:], 0)
		if i < 0 {
			s := string(b[p:])
			p = len(b)
			return s, true
		}
		s := string(b[p : p+i])
		p += i + 1
		return s, true
	}
	exe, ok := next()
	if !ok || argc < 0 {
		return "", nil, errors.New("malformed kern.procargs2 buffer")
	}
	for p < len(b) && b[p] == 0 {
		p++
	}
	for i := 0; i < argc; i++ {
		if _, ok := next(); !ok {
			return exe, nil, errors.New("kern.procargs2: truncated arguments")
		}
	}
	// A process that set its title (node, and so npm, do through libuv)
	// has written the title over its arguments and zeroed the rest of that
	// area, so fewer arguments than argc are left: skip the NUL run.
	for p < len(b) && b[p] == 0 {
		p++
	}
	env = map[string]string{}
	for {
		s, ok := next()
		if !ok || s == "" {
			break
		}
		if k, v, ok := cutEq(s); ok {
			if _, dup := env[k]; !dup {
				env[k] = v
			}
		}
	}
	return exe, env, nil
}

func cutEq(s string) (string, string, bool) {
	i := bytes.IndexByte([]byte(s), '=')
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// parseLsofPeer reads `lsof -F pun` output for the socket named
// 127.0.0.1:cport->127.0.0.1:sport.
func parseLsofPeer(out []byte, name string) (pid, uid int, ok bool) {
	cur, curUID := 0, -1
	for _, l := range bytes.Split(out, []byte("\n")) {
		if len(l) < 2 {
			continue
		}
		v := string(l[1:])
		switch l[0] {
		case 'p':
			cur, curUID = atoi(v), -1
		case 'u':
			curUID = atoi(v)
		case 'n':
			if v == name && cur > 0 && curUID >= 0 {
				return cur, curUID, true
			}
		}
	}
	return 0, 0, false
}

// parseLsofName returns the first n field of `lsof -F n` output.
func parseLsofName(out []byte) string {
	for _, l := range bytes.Split(out, []byte("\n")) {
		if len(l) > 1 && l[0] == 'n' {
			return string(l[1:])
		}
	}
	return ""
}

func atoi(s string) int {
	n := 0
	if s == "" {
		return -1
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
