package npm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/procinfo"
)

// peerLookup finds the npm process on the other end of a request's
// connection (tests replace it).
var peerLookup = procinfo.Peer

// peerClient identifies the process on the other end of r's TCP connection
// (Linux: /proc; macOS: sysctl/proc_info, see internal/procinfo) and reads
// its environment and working directory. found is false if it cannot be
// determined: the caller then uses the daemon's own view (user and global
// npmrc, its own environment), as on systems without a backend. sameUser is
// false if the process belongs to another user.
func peerClient(r *http.Request) (c *client, sameUser, found bool) {
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	ra, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if la == nil || err != nil {
		return nil, false, false
	}
	key := r.RemoteAddr
	if v, ok := peerCache.Load(key); ok {
		e := v.(peerEntry)
		if time.Since(e.at) < peerTTL {
			return e.c, e.same, e.c != nil || !e.same
		}
	}
	start := time.Now()
	p, err := peerLookup(la, ra)
	took := time.Since(start)
	var other *procinfo.OtherUserError
	switch {
	case errors.As(err, &other):
		peerStore(key, peerEntry{at: time.Now()})
		logPeer("warn", "refused", took, map[string]any{"uid": other.UID},
			fmt.Sprintf("%s request: %v; no login is attached", r.Method, err))
		return nil, false, true
	case errors.Is(err, procinfo.ErrSelf):
		return nil, true, false
	case errors.Is(err, procinfo.ErrUnsupported):
		peerStore(key, peerEntry{same: true, at: time.Now()})
		logPeer("debug", "fallback", took, nil, fmt.Sprintf("%v: using the user and global npmrc", err))
		return nil, true, false
	case err != nil:
		peerStore(key, peerEntry{same: true, at: time.Now()})
		logPeer("warn", "fallback", took, nil,
			fmt.Sprintf("%s request: cannot read the calling npm process (%v); using the user and global npmrc and the daemon's environment", r.Method, err))
		return nil, true, false
	}
	c = &client{env: p.Env, cwd: p.Cwd, node: p.Exe}
	fields := map[string]any{"pid": p.PID, "via": p.Via, "env_vars": len(p.Env)} // a count, never a value
	if p.CwdErr != nil {
		logPeer("warn", "partial", took, fields,
			fmt.Sprintf("%s request: working directory of npm (pid %d) unreadable (%v); its project .npmrc is not used", r.Method, p.PID, p.CwdErr))
	} else {
		logPeer("debug", "ok", took, fields, fmt.Sprintf("%s request: npm process pid %d found via %s", r.Method, p.PID, p.Via))
	}
	peerStore(key, peerEntry{c: c, same: true, at: time.Now()})
	return c, true, true
}

// logPeer records a peer lookup (never an environment value: only pids,
// uids, paths and error text, redacted by the log store).
func logPeer(level, result string, took time.Duration, fields map[string]any, msg string) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["os"] = runtime.GOOS
	fields["duration_us"] = took.Microseconds()
	logstore.LogQuiet(logstore.Entry{Event: logstore.EvPeerLookup, Level: level, Ecosystem: "npm", Result: result,
		DurationMS: took.Milliseconds(), Fields: fields, Message: msg})
}

type peerEntry struct {
	c    *client
	same bool
	at   time.Time
}

const peerTTL = 10 * time.Second

var (
	peerCache sync.Map // RemoteAddr -> peerEntry (one lookup per connection)
	peerPrune struct {
		sync.Mutex
		last time.Time
	}
)

// peerStore caches e and drops expired entries now and then.
func peerStore(key string, e peerEntry) {
	peerCache.Store(key, e)
	peerPrune.Lock()
	defer peerPrune.Unlock()
	if time.Since(peerPrune.last) < peerTTL {
		return
	}
	peerPrune.last = time.Now()
	peerCache.Range(func(k, v any) bool {
		if time.Since(v.(peerEntry).at) >= peerTTL {
			peerCache.Delete(k)
		}
		return true
	})
}
