package daemon

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

func TestEventRingSinceAndCap(t *testing.T) {
	r := newEventRing(3)
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		r.add(logstore.Entry{TS: t0.Add(time.Duration(i) * time.Millisecond), Message: strconv.Itoa(i)})
	}
	all := r.since(time.Time{})
	if len(all) != 3 || all[0].Message != "2" || all[2].Message != "4" {
		t.Fatalf("cap: %+v", all)
	}
	if got := r.since(t0.Add(3 * time.Millisecond)); len(got) != 1 || got[0].Message != "4" {
		t.Fatalf("since: %+v", got)
	}
	mux := http.NewServeMux()
	mountEvents(mux, r)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/-/consona/events?since=" + strconv.FormatInt(t0.Add(2*time.Millisecond).UnixNano(), 10))
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// Entries reach the ring through the logstore tee already redacted.
func TestEventRingGetsRedactedEntries(t *testing.T) {
	r := newEventRing(10)
	defer logstore.AddTee(r.add)()
	tok := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	logstore.Log(logstore.Entry{Event: logstore.EvCredentialResolve, Message: "token " + tok})
	got := r.since(time.Time{})
	if len(got) != 1 || got[0].Message == "token "+tok {
		t.Fatalf("not redacted: %+v", got)
	}
}
