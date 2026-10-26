package localcli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/source/chain"
	"github.com/tirthpatel002/consona-package-manager/internal/source/sshremote"
)

// Resolutions (local, ssh, github), failures and ssh probe failures reach
// the event log, redacted.
func TestLogSinkRecordsSourcesRedacted(t *testing.T) {
	st, err := logstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := logstore.NewAsync(st)
	logstore.SetDefault(a, false)
	defer logstore.SetDefault(nil, false)
	fake := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	var got []string
	s := logSink{also: sinkFunc(func(r chain.Resolution) { got = append(got, r.Kind) })}
	s.Resolved(chain.Resolution{Name: "@me/lib", Version: "1.0.0-local.1.habc", Kind: "local", Where: "/src/lib", Device: "box"})
	s.Resolved(chain.Resolution{Name: "@me/tool", Version: "2.0.0", Kind: "ssh", Where: "vm (u@vm)", Device: "vm"})
	s.Resolved(chain.Resolution{Name: "@acme/x", Version: "3.0.0", Kind: "github", Where: "github.com/acme/x"})
	s.ResolveFailed("@acme/nope", []string{"this device's registered folders: not registered"},
		fmt.Errorf("@acme/nope: %w (token %s)", core.ErrNotFound, fake))
	logProbe(sshremote.Remote{Name: "bad", Target: "u@192.0.2.1"}, sshremote.State{Err: "ssh: connect to host 192.0.2.1: timed out"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.Flush(ctx)
	es, err := st.Query(logstore.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "local,ssh,github" {
		t.Fatalf("also sink: %v", got)
	}
	kinds := map[string]bool{}
	var all strings.Builder
	for _, e := range es {
		fmt.Fprintf(&all, "%+v\n", e)
		if e.Event == logstore.EvSourceResolve {
			kinds[fmt.Sprint(e.Fields["source"])] = true
		}
	}
	if !kinds["local"] || !kinds["ssh"] || !kinds["github"] {
		t.Fatalf("resolutions: %v\n%s", kinds, all.String())
	}
	for _, want := range []string{"source.fail", "not-found", "ssh.probe", "unreachable", "192.0.2.1"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("missing %q in\n%s", want, all.String())
		}
	}
	if strings.Contains(all.String(), fake) {
		t.Fatal("token value reached the log store")
	}
}
