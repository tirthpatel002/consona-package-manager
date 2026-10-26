package logstore

import "sync"

var (
	teeMu  sync.RWMutex
	teeSeq int
	tees   = map[int]func(Entry){}
)

// AddTee makes f see every entry logged in this process (after
// redaction, so f never sees a token value), whether or not a store is
// open. The CLI uses it to print live progress; the daemon keeps recent
// entries for `consona install` to stream. It returns the remove func.
func AddTee(f func(Entry)) (remove func()) {
	teeMu.Lock()
	teeSeq++
	id := teeSeq
	tees[id] = f
	teeMu.Unlock()
	return func() {
		teeMu.Lock()
		delete(tees, id)
		teeMu.Unlock()
	}
}

func callTees(e Entry) {
	teeMu.RLock()
	fs := make([]func(Entry), 0, len(tees))
	for _, f := range tees {
		fs = append(fs, f)
	}
	teeMu.RUnlock()
	for _, f := range fs {
		f(e)
	}
}
