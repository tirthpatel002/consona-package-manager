package npm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const llBase = "http://127.0.0.1:1/npm/"

func llEnv(t *testing.T, orig string) string {
	t.Helper()
	t.Setenv("CONSONA_HOME", t.TempDir())
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	if orig != "" {
		if err := os.WriteFile(npmrc, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("npm_config_userconfig", npmrc)
	return npmrc
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

// stripComments is what npm does when it saves the npmrc (npm login,
// npm config set): comment lines (consona's block markers) disappear.
func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Setup adds loglevel=verbose as a consona line when the user has none,
// records it in npm-setup.json, and undo removes it.
func TestSetupAddsConsonaLoglevel(t *testing.T) {
	orig := "registry=https://mirror.example/\ncolor=false\n"
	npmrc := llEnv(t, orig)
	if _, err := (Adapter{}).Setup(context.Background(), llBase, []string{"o"}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, npmrc)
	if strings.Count(got, "\nloglevel=verbose\n") != 1 || !loadState().LoglevelAdded || !OwnsLoglevel() {
		t.Fatalf("loglevel not added/recorded:\n%s\nstate %+v", got, loadState())
	}
	// Setup again (and again after npm stripped the markers): still one line.
	if _, err := (Adapter{}).Setup(context.Background(), llBase, []string{"p"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(npmrc, []byte(stripComments(readFile(t, npmrc))), 0o600)
	if _, err := (Adapter{}).Setup(context.Background(), llBase, []string{"q"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, npmrc); strings.Count(got, "loglevel=") != 1 || !loadState().LoglevelAdded {
		t.Fatalf("re-setup duplicated or lost loglevel:\n%s", got)
	}
	if _, err := (Adapter{}).Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, npmrc); got != orig {
		t.Fatalf("undo:\n%q\nwant\n%q", got, orig)
	}
}

// The user's own loglevel (any value, verbose included) is never touched:
// setup adds nothing and undo keeps it.
func TestSetupKeepsUserLoglevel(t *testing.T) {
	for _, lvl := range []string{"loglevel=warn", "loglevel=verbose", "loglevel = silly"} {
		orig := "color=false\n" + lvl + "\n"
		npmrc := llEnv(t, orig)
		if _, err := (Adapter{}).Setup(context.Background(), llBase, []string{"o"}); err != nil {
			t.Fatal(err)
		}
		got := readFile(t, npmrc)
		if strings.Count(got, "loglevel") != 1 || loadState().LoglevelAdded || OwnsLoglevel() {
			t.Fatalf("%s: setup touched the user's loglevel:\n%s", lvl, got)
		}
		os.WriteFile(npmrc, []byte(stripComments(got)), 0o600)
		(Adapter{}).Teardown(context.Background())
		if got := readFile(t, npmrc); got != orig {
			t.Fatalf("%s: undo:\n%q\nwant\n%q", lvl, got, orig)
		}
	}
}

// If the user sets their own loglevel after setup, the next setup drops
// consona's line and undo leaves theirs alone.
func TestUserLoglevelAfterSetupWins(t *testing.T) {
	npmrc := llEnv(t, "color=false\n")
	(Adapter{}).Setup(context.Background(), llBase, []string{"o"})
	os.WriteFile(npmrc, []byte(stripComments(readFile(t, npmrc))+"loglevel=warn\n"), 0o600)
	(Adapter{}).Setup(context.Background(), llBase, []string{"o"})
	got := readFile(t, npmrc)
	if strings.Contains(got, "loglevel=verbose") || !strings.Contains(got, "loglevel=warn") || loadState().LoglevelAdded {
		t.Fatalf("\n%s", got)
	}
	(Adapter{}).Teardown(context.Background())
	if got := readFile(t, npmrc); got != "color=false\nloglevel=warn\n" {
		t.Fatalf("undo:\n%q", got)
	}
}

// Regression: setup, then npm rewrites the npmrc without comments (all
// consona markers gone), then teardown leaves exactly the user's lines.
func TestTeardownAfterNpmStripsMarkers(t *testing.T) {
	orig := "registry=https://mirror.example/\ncolor=false\n//mirror.example/:_authToken=${NPM_TOKEN}\nfund=false\n"
	npmrc := llEnv(t, orig)
	if _, err := (Adapter{}).Setup(context.Background(), llBase, []string{"o", "acme"}); err != nil {
		t.Fatal(err)
	}
	stripped := stripComments(readFile(t, npmrc))
	if strings.Contains(stripped, "#") || !strings.Contains(stripped, "loglevel=verbose") {
		t.Fatalf("test setup: %q", stripped)
	}
	os.WriteFile(npmrc, []byte(stripped), 0o600)
	if _, err := (Adapter{}).Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, npmrc); got != orig {
		t.Fatalf("teardown after npm stripped markers:\n%q\nwant\n%q", got, orig)
	}
}

func TestSetupNpmrcPure(t *testing.T) {
	out, added, err := SetupNpmrc("color=false\n", llBase, []string{"o"}, false)
	if err != nil || !added || !strings.HasSuffix(out, "\nloglevel=verbose\n"+blockEnd+"\n") {
		t.Fatalf("%v %v\n%s", added, err, out)
	}
	// Marker-less consona line (npm rewrote the file) is recognised by state.
	out2, added, _ := SetupNpmrc(stripComments(out), llBase, []string{"o"}, true)
	if !added || strings.Count(out2, "loglevel=") != 1 {
		t.Fatalf("\n%s", out2)
	}
	// Without the state flag the same line is the user's.
	out3, added, _ := SetupNpmrc(stripComments(out), llBase, []string{"o"}, false)
	if added || strings.Count(out3, "loglevel=") != 1 {
		t.Fatalf("\n%s", out3)
	}
}
