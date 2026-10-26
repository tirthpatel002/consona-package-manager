package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// eventRing keeps the daemon's most recent structured log entries (already
// redacted) so `consona install` and `consona audit` can print what the
// daemon does while npm runs.
type eventRing struct {
	mu  sync.Mutex
	buf []logstore.Entry
	max int
}

func newEventRing(max int) *eventRing { return &eventRing{max: max} }

func (r *eventRing) add(e logstore.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, e)
	if len(r.buf) > r.max {
		r.buf = append([]logstore.Entry(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

// since returns the entries logged strictly after t, oldest first.
func (r *eventRing) since(t time.Time) []logstore.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []logstore.Entry{}
	for _, e := range r.buf {
		if e.TS.After(t) {
			out = append(out, e)
		}
	}
	return out
}

func mountEvents(mux *http.ServeMux, r *eventRing) {
	mux.HandleFunc("/-/consona/events", func(w http.ResponseWriter, req *http.Request) {
		ns, _ := strconv.ParseInt(req.URL.Query().Get("since"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.since(time.Unix(0, ns)))
	})
}

// EventsSince fetches the structured events the daemon logged after t
// (nil if the daemon is down or too old to have the endpoint).
func EventsSince(c Config, t time.Time) []logstore.Entry {
	resp, err := client.Get(fmt.Sprintf("%s/-/consona/events?since=%d", c.BaseURL(), t.UnixNano()))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out []logstore.Entry
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// Health reports whether a daemon answers on c's port and whether it is
// this binary's (same build and home).
func Health(c Config) (up, current bool) {
	id, up := ping(c)
	return up, up && id == c.identity()
}
