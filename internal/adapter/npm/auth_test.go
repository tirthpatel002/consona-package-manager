package npm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The login for the upstream is found as npm finds it: env > project >
// user > global, last duplicate wins, ${VAR} expanded from npm's env.
func TestLoginLookupLikeNpm(t *testing.T) {
	d := t.TempDir()
	proj := filepath.Join(d, "proj")
	os.MkdirAll(filepath.Join(proj, "src"), 0o755)
	os.WriteFile(filepath.Join(proj, "package.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(d, "prefix", "etc"), 0o755)
	os.MkdirAll(filepath.Join(d, "prefix", "bin"), 0o755)
	write := func(p, s string) { os.WriteFile(p, []byte(s), 0o600) }
	user, global, project := filepath.Join(d, "user"), filepath.Join(d, "prefix", "etc", "npmrc"), filepath.Join(proj, ".npmrc")
	c := &client{env: map[string]string{"npm_config_userconfig": user, "NPM_TOKEN": "from-env-ref"}, cwd: filepath.Join(proj, "src"), node: filepath.Join(d, "prefix", "bin", "node")}
	reg := "https://reg.example/"
	check := func(want string) {
		t.Helper()
		if got := c.loginFor(reg).header; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	check("")
	write(global, "//reg.example/:_authToken=global\n")
	check("Bearer global")
	write(user, "//reg.example/:_authToken=old\n//reg.example/:_authToken=user\n")
	check("Bearer user")
	write(project, "//reg.example/:_authToken=${NPM_TOKEN}\n")
	check("Bearer from-env-ref")
	c.env["NPM_CONFIG_//reg.example/:_authToken"] = "env"
	check("Bearer env")
	delete(c.env, "NPM_CONFIG_//reg.example/:_authToken")
	os.Remove(project)
	os.Remove(global) // npm merges per key: a global token would still win
	write(user, "//reg.example/:username=me\n//reg.example/:_password=cGFzcw==\n")
	check("Basic bWU6cGFzcw==")
}

// npm logout through consona while npm holds the placeholder revokes the
// real upstream token and removes it from the user npmrc; with no token it
// is a local no-op.
func TestLogoutRevokesRealToken(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	}))
	defer up.Close()
	srv, _ := newServer(t)
	srv.Config.Handler.(*handler).upstreamURL = up.URL
	npmrc := os.Getenv("npm_config_userconfig")
	b, _ := os.ReadFile(npmrc)
	line := strings.TrimPrefix(up.URL, "http:") + "/:_authToken=tok1"
	os.WriteFile(npmrc, append(b, []byte(line+"\n")...), 0o600)
	del := func() int {
		req, _ := http.NewRequest("DELETE", srv.URL+"/-/user/token/"+Placeholder, nil)
		req.Header.Set("Authorization", "Bearer "+Placeholder)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := del(); c != 200 || len(seen) != 1 || seen[0] != "DELETE /-/user/token/tok1 Bearer tok1" {
		t.Fatalf("%d %v", c, seen)
	}
	// npm then saves its config without the placeholder line.
	b, _ = os.ReadFile(npmrc)
	os.WriteFile(npmrc, []byte(strings.ReplaceAll(string(b), "_authToken="+Placeholder, "x=y")), 0o600)
	time.Sleep(300 * time.Millisecond)
	if b, _ := os.ReadFile(npmrc); strings.Contains(string(b), "tok1") || !strings.Contains(string(b), "@o:registry=") {
		t.Fatalf("token not forgotten or routing lost:\n%s", b)
	}
	if c := del(); c != 200 || len(seen) != 1 {
		t.Fatalf("second logout must be local: %d %v", c, seen)
	}
}
