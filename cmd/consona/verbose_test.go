package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

func captureSay(t *testing.T, q bool) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldQ := vout, quiet
	vout, quiet = &buf, q
	t.Cleanup(func() { vout, quiet = oldOut, oldQ })
	return &buf
}

func TestParseQuiet(t *testing.T) {
	t.Setenv("CONSONA_QUIET", "")
	args, q := parseQuiet([]string{"install", "--quiet", "lodash"})
	if !q || !reflect.DeepEqual(args, []string{"install", "lodash"}) {
		t.Fatalf("%v %v", args, q)
	}
	// After `--` it belongs to npm.
	args, q = parseQuiet([]string{"install", "--", "--quiet"})
	if q || !reflect.DeepEqual(args, []string{"install", "--", "--quiet"}) {
		t.Fatalf("%v %v", args, q)
	}
	if _, q = parseQuiet([]string{"install"}); q {
		t.Fatal("quiet by default")
	}
	for _, v := range []string{"1", "true", "yes"} {
		t.Setenv("CONSONA_QUIET", v)
		if _, q = parseQuiet([]string{"install"}); !q {
			t.Fatalf("CONSONA_QUIET=%s not quiet", v)
		}
	}
	for _, v := range []string{"0", "false"} {
		t.Setenv("CONSONA_QUIET", v)
		if _, q = parseQuiet([]string{"install"}); q {
			t.Fatalf("CONSONA_QUIET=%s quiet", v)
		}
	}
}

func TestQuietSilencesProgress(t *testing.T) {
	buf := captureSay(t, true)
	say("starting %s", "daemon")
	sayEvent(logstore.Entry{Event: logstore.EvSourceResolve, Level: "info", Message: "x"})
	sayNpmrcDiff("/x", "", "a=b\n")
	if buf.Len() != 0 {
		t.Fatalf("quiet printed:\n%s", buf)
	}
	buf = captureSay(t, false)
	say("starting %s", "daemon")
	sayEvent(logstore.Entry{Event: logstore.EvSourceTagMoved, Level: "warn", Package: "@o/p", Message: "tag moved"})
	sayEvent(logstore.Entry{Event: logstore.EvRegistryRedirect, Level: "debug", Message: "noise"})
	out := buf.String()
	if !strings.Contains(out, "consona: starting daemon") || !strings.Contains(out, "[source.tag_moved] WARN pkg=@o/p") || strings.Contains(out, "noise") {
		t.Fatalf("verbose output:\n%s", out)
	}
}

// Token values never appear: not in say lines, not in npmrc diffs.
func TestVerboseNeverPrintsTokens(t *testing.T) {
	buf := captureSay(t, false)
	tok := "ghp_" + strings.Repeat("a1B2", 9)
	say("using %s", tok)
	sayNpmrcDiff("/npmrc", "color=false\n", "color=false\n//r.example/:_authToken=npm_secretvalue123\n//127.0.0.1:1/npm/:_authToken="+tok+"\n_auth=c2VjcmV0c2VjcmV0\n")
	out := buf.String()
	for _, s := range []string{tok, "npm_secretvalue123", "c2VjcmV0c2VjcmV0"} {
		if strings.Contains(out, s) {
			t.Fatalf("token printed:\n%s", out)
		}
	}
	if !strings.Contains(out, "+ //r.example/:_authToken=[redacted]") || strings.Contains(out, "color=false") {
		t.Fatalf("diff:\n%s", out)
	}
}

func TestNpmrcDiffShowsAddedAndRemoved(t *testing.T) {
	buf := captureSay(t, false)
	sayNpmrcDiff("/npmrc", "color=false\nloglevel=verbose\n@o:registry=http://127.0.0.1:1/npm/\n", "color=false\n")
	out := buf.String()
	if !strings.Contains(out, "- loglevel=verbose") || !strings.Contains(out, "- @o:registry=") || strings.Contains(out, "color") {
		t.Fatalf("\n%s", out)
	}
}

func TestNpmArgs(t *testing.T) {
	t.Setenv("npm_config_loglevel", "")
	t.Setenv("NPM_CONFIG_LOGLEVEL", "")
	cases := []struct {
		in             []string
		quiet, consona bool
		want           []string
	}{
		{[]string{"install"}, false, true, []string{"install", "--verbose"}},
		{[]string{"audit", "--json"}, false, false, []string{"audit", "--json", "--verbose"}},
		{[]string{"install", "--loglevel=warn"}, false, true, []string{"install", "--loglevel=warn"}},
		{[]string{"install", "--loglevel", "silly"}, false, true, []string{"install", "--loglevel", "silly"}},
		{[]string{"install", "-s"}, false, true, []string{"install", "-s"}},
		{[]string{"install", "--silent"}, true, true, []string{"install", "--silent"}},
		{[]string{"install"}, true, true, []string{"install", "--loglevel=notice"}},
		{[]string{"install"}, true, false, []string{"install"}},
	}
	for _, c := range cases {
		if got := npmArgs(append([]string(nil), c.in...), c.quiet, c.consona); !reflect.DeepEqual(got, c.want) {
			t.Errorf("npmArgs(%v, quiet=%v, consona=%v) = %v, want %v", c.in, c.quiet, c.consona, got, c.want)
		}
	}
	t.Setenv("npm_config_loglevel", "warn")
	if got := npmArgs([]string{"install"}, false, true); !reflect.DeepEqual(got, []string{"install"}) {
		t.Errorf("env loglevel: %v", got)
	}
}
