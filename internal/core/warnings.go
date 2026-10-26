package core

import (
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// WarnLogEvery is how often the same warning (same key) is written to the
// daemon log and log store again; in between it is only recorded for the
// CLI's install summary.
var WarnLogEvery = 10 * time.Minute

// warnings is process-wide: sources (e.g. a GitHub redirect) and adapters
// (e.g. a package.json name that differs from the requested one) report
// here, and the daemon serves them to `consona install`'s summary.
var warnings = struct {
	mu     sync.Mutex
	list   []reported
	logged map[string]time.Time
	msgs   map[string]string // key -> last message (see Touch)
}{logged: map[string]time.Time{}, msgs: map[string]string{}}

// Warn records a user-facing warning. ev.Message is the text shown to the
// user; ev is logged at level warn (daemon log + log store) at most once
// per key per WarnLogEvery, and recorded for WarningsSince every time.
func Warn(key string, ev logstore.Entry) {
	now := time.Now()
	warnings.mu.Lock()
	warnings.list = append(warnings.list, reported{now, ev.Message})
	if len(warnings.list) > 200 {
		warnings.list = warnings.list[len(warnings.list)-200:]
	}
	warnings.msgs[key] = ev.Message
	t, seen := warnings.logged[key]
	log := !seen || now.Sub(t) >= WarnLogEvery
	if log {
		warnings.logged[key] = now
	}
	warnings.mu.Unlock()
	if log {
		ev.Level = "warn"
		logstore.Log(ev)
	}
}

// WarningsSince returns distinct warnings recorded after t.
func WarningsSince(t time.Time) []string {
	warnings.mu.Lock()
	defer warnings.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, r := range warnings.list {
		if r.at.After(t) && !seen[r.msg] {
			seen[r.msg] = true
			out = append(out, r.msg)
		}
	}
	return out
}

// Touch re-records the last warning reported under key, if any, for
// WarningsSince (nothing is logged). Adapters call it for warnings found
// by a source when the source was not asked again (memoised versions), so
// every install summary still shows them.
func Touch(key string) {
	warnings.mu.Lock()
	defer warnings.mu.Unlock()
	if m, ok := warnings.msgs[key]; ok {
		warnings.list = append(warnings.list, reported{time.Now(), m})
	}
}

// RedirectKey is the warning key of a renamed/transferred source repo.
func RedirectKey(owner, repo string) string { return "redirect|" + strings.ToLower(owner+"/"+repo) }
