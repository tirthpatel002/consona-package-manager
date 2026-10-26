package localcli

import (
	"errors"
	"strings"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/localindex"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
	"github.com/tirthpatel002/consona-package-manager/internal/source/chain"
	"github.com/tirthpatel002/consona-package-manager/internal/source/github"
	"github.com/tirthpatel002/consona-package-manager/internal/source/sshremote"
)

// logSink records chain resolutions and failures in the event log
// (logstore redacts every field). also, if set, receives them too.
type logSink struct{ also chain.EventSink }

func split(name string) (string, string) {
	s, p, _ := localindex.SplitName(strings.ToLower(name))
	return s, p
}

func (l logSink) Resolved(r chain.Resolution) {
	if l.also != nil {
		l.also.Resolved(r)
	}
	scope, pkg := split(r.Name)
	f := map[string]any{"source": r.Kind, "where": r.Where}
	if r.Device != "" {
		f["device"] = r.Device
	}
	logstore.Log(logstore.Entry{Event: logstore.EvSourceResolve, Level: "info", Ecosystem: "npm", Package: r.Name,
		Owner: scope, Repo: pkg, Version: r.Version, Result: "ok", Message: r.Kind + " " + r.Where, Fields: f})
}

func (l logSink) ResolveFailed(name string, tried []string, err error) {
	if fs, ok := l.also.(chain.FailureSink); ok {
		fs.ResolveFailed(name, tried, err)
	}
	scope, pkg := split(name)
	logstore.Log(logstore.Entry{Event: logstore.EvSourceFail, Level: "warn", Ecosystem: "npm", Package: name,
		Owner: scope, Repo: pkg, Result: "fail", ErrorCode: errCode(err), Message: err.Error(),
		Fields: map[string]any{"tried": tried}})
}

func errCode(err error) string {
	var ae *github.AccessError
	switch {
	case errors.Is(err, core.ErrNotFound):
		return "not-found"
	case errors.As(err, &ae):
		return ae.Code()
	case errors.Is(err, sshremote.ErrNoConsona):
		return "no-consona"
	}
	return "error"
}

func remoteFields(r sshremote.Remote) map[string]any {
	f := map[string]any{"remote": r.Name, "target": r.Target, "auto": r.Auto}
	if r.Port != 0 {
		f["port"] = r.Port
	}
	return f
}

// logProbe records ssh probes: failures at warn (with ssh's explanation),
// successes quietly at debug.
func logProbe(r sshremote.Remote, st sshremote.State) {
	f := remoteFields(r)
	e := logstore.Entry{Event: logstore.EvSSHProbe, DurationMS: st.Latency.Milliseconds(), Fields: f}
	if st.OK {
		f["packages"] = len(st.Names)
		e.Level, e.Result, e.Message = "debug", "ok", "remote "+r.Label()+" reachable"
		logstore.LogQuiet(e)
		return
	}
	f["no_consona"] = st.NoConsona
	e.Level, e.Result, e.Message = "warn", "fail", "remote "+r.Label()+": "+st.Err
	e.ErrorCode = "unreachable"
	if st.NoConsona {
		e.ErrorCode = "no-consona"
	}
	logstore.Log(e)
}
