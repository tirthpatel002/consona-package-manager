package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

type oneTagSource struct{ commit string }

func (s *oneTagSource) Describe(r Ref) string { return "test/" + r.String() }
func (s *oneTagSource) Revisions(context.Context, Ref) ([]Revision, error) {
	return RevisionsFromTags([]Tag{{Name: "v1.0.0", Commit: s.commit}}, ""), nil
}
func (s *oneTagSource) Fetch(_ context.Context, _ Ref, _, dir string) error {
	return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@o/x","version":"1.0.0"}`), 0o644)
}

type testBuilder struct{ out string }

func (testBuilder) Ecosystem() string { return "npm" }
func (testBuilder) Inspect(string) (*Info, error) {
	return &Info{Version: "1.0.0", Manifest: []byte(`{"name":"@o/x"}`), NeedsBuild: true}, nil
}
func (b testBuilder) Build(_ context.Context, _ string, r BuildRequest) (*Artifact, error) {
	if b.out != "" {
		return nil, &BuildError{Step: "npm install", Script: `"prepare" script`, Output: []byte(b.out)}
	}
	if err := os.WriteFile(filepath.Join(r.OutDir, "x.tgz"), []byte("t"), 0o644); err != nil {
		return nil, err
	}
	return &Artifact{Version: r.Version, File: "x.tgz", SHA1: "s1", SHA512: "s512"}, nil
}

// A failed build names the failing script, shows the last BuildTailLines
// lines of output without credentials, and points at the full log.
func TestBuildFailureMessage(t *testing.T) {
	t.Setenv("MY_DEPLOY_TOKEN", "value-of-a-deploy-token-0001")
	var out strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&out, "out-%02d\n", i)
	}
	out.WriteString("leak ghs_" + strings.Repeat("Q", 36) + " value-of-a-deploy-token-0001\n")
	e := NewEngine(&oneTagSource{"aaaaaaa1"}, t.TempDir())
	ref := Ref{Owner: "o", Name: "x"}
	b := testBuilder{out: out.String()}
	pkgs, err := e.Packages(context.Background(), b, ref, "@o/x")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Build(context.Background(), b, ref, "@o/x", pkgs[0])
	if !errors.Is(err, ErrBuild) {
		t.Fatalf("want ErrBuild, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{`build failed for @o/x@1.0.0 (commit aaaaaaa): the package's "prepare" script failed (during npm install)`, "  | out-30", "  | out-12", "full output: "} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "out-11") || strings.Contains(msg, "QQQQ") || strings.Contains(msg, "value-of-a-deploy-token") {
		t.Fatalf("message must have only the last %d lines, no credentials:\n%s", BuildTailLines, msg)
	}
	logf := msg[strings.LastIndex(msg, " ")+1:]
	bs, _ := os.ReadFile(logf)
	if !strings.Contains(string(bs), "out-01") || strings.Contains(string(bs), "QQQQ") || strings.Contains(string(bs), "value-of-a-deploy-token") {
		t.Fatalf("log %s: full output without credentials expected:\n%s", logf, bs)
	}
}

// MovedFrom finds an earlier build of the same version from another
// commit (the tag was moved).
func TestMovedFrom(t *testing.T) {
	dir := t.TempDir()
	ref := Ref{Owner: "o", Name: "x"}
	for _, c := range []string{"aaaaaaa1", "bbbbbbb2"} {
		e := NewEngine(&oneTagSource{c}, dir)
		pkgs, err := e.Packages(context.Background(), testBuilder{}, ref, "@o/x")
		if err != nil {
			t.Fatal(err)
		}
		if got := e.MovedFrom(testBuilder{}, ref, pkgs[0]); c == "aaaaaaa1" && len(got) != 0 {
			t.Fatalf("first build: moved from %v", got)
		}
		if _, err := e.Build(context.Background(), testBuilder{}, ref, "@o/x", pkgs[0]); err != nil {
			t.Fatal(err)
		}
		if got := e.MovedFrom(testBuilder{}, ref, pkgs[0]); c == "bbbbbbb2" && (len(got) != 1 || got[0] != "aaaaaaa1") {
			t.Fatalf("after the tag moved: %v", got)
		}
	}
}

// Warnings are recorded every time (install summary) but logged once per
// key per WarnLogEvery.
func TestWarnDedupesLogging(t *testing.T) {
	t.Setenv("CONSONA_LOG_BACKEND", "jsonl")
	st, err := logstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := logstore.NewAsync(st)
	logstore.SetDefault(a, false)
	t.Cleanup(func() { logstore.SetDefault(nil, false); a.Close() })
	start := time.Now()
	for i := 0; i < 3; i++ {
		Warn("k1", logstore.Entry{Event: logstore.EvSourceRedirect, Message: "renamed"})
	}
	Warn("k2", logstore.Entry{Event: logstore.EvSourceRedirect, Message: "other"})
	if w := WarningsSince(start); len(w) != 2 || w[0] != "renamed" || w[1] != "other" {
		t.Fatalf("WarningsSince %q", w)
	}
	if w := WarningsSince(time.Now()); len(w) != 0 {
		t.Fatalf("nothing after now: %q", w)
	}
	a.Flush(context.Background())
	es, _ := st.Query(logstore.Filter{Event: logstore.EvSourceRedirect})
	if len(es) != 2 || es[0].Level != "warn" {
		t.Fatalf("want 2 warn events, got %+v", es)
	}
}
