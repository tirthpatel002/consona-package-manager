// Command fakeregistry is a tiny npm registry for the e2e test that, like
// a private registry or authenticated mirror, requires a token for
// everything. It stands in for "the user's previous registry" so the test
// never publishes to registry.npmjs.org.
//
// Usage: FAKE_TOKEN=... [FAKE_TOKEN2=... FAKE_TOKEN3=...] fakeregistry -addr 127.0.0.1:PORT -log FILE
// Tokens 1-3 belong to e2e-user, e2e-project-user, e2e-env-user.
// Publishing a name starting with "otp-" needs the one-time password
// 123456 (npm-otp header), like an account with 2FA.
// The log has one line per request: METHOD PATH user=... otp=...; never tokens.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:48799", "listen address")
	logPath := flag.String("log", "", "request log file")
	flag.Parse()
	base := "http://" + *addr
	tokens := map[string]string{os.Getenv("FAKE_TOKEN"): "e2e-user"}
	for k, u := range map[string]string{"FAKE_TOKEN2": "e2e-project-user", "FAKE_TOKEN3": "e2e-env-user"} {
		if v := os.Getenv(k); v != "" {
			tokens[v] = u
		}
	}
	if os.Getenv("FAKE_TOKEN") == "" {
		log.Fatal("FAKE_TOKEN not set")
	}
	var mu sync.Mutex
	published := map[string]bool{}
	lf := io.Writer(os.Stderr)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		lf = f
	}
	tgz := tarball(`{"name":"@acme/secret","version":"1.0.0","main":"index.js"}`, `module.exports = "auth-only package";`+"\n")
	s1 := sha1.Sum(tgz)
	s512 := sha512.Sum512(tgz)
	packument := map[string]any{
		"name": "@acme/secret", "dist-tags": map[string]string{"latest": "1.0.0"},
		"versions": map[string]any{"1.0.0": map[string]any{
			"name": "@acme/secret", "version": "1.0.0", "main": "index.js",
			"dist": map[string]string{
				"tarball":   base + "/@acme/secret/-/secret-1.0.0.tgz",
				"shasum":    hex.EncodeToString(s1[:]),
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
			},
		}},
	}
	js := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p, _ := url.PathUnescape(r.URL.EscapedPath())
		mu.Lock()
		defer mu.Unlock()
		user := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		logged := p
		if i := strings.Index(logged, "/-/user/token/"); i >= 0 {
			logged = logged[:i] + "/-/user/token/[redacted]"
		}
		otp := r.Header.Get("npm-otp")
		fmt.Fprintf(lf, "%s %s %s auth=%v user=%s otp=%v\n", time.Now().Format("15:04:05"), r.Method, logged, user != "", user, otp != "")
		// Login endpoints work without a token (that's how you get one).
		switch {
		case r.Method == "POST" && p == "/-/v1/login":
			js(w, 200, map[string]string{"loginUrl": base + "/login", "doneUrl": base + "/-/v1/done"})
			return
		case r.Method == "GET" && p == "/-/v1/done":
			tok := fmt.Sprintf("fake-web-%d", time.Now().UnixNano())
			tokens[tok] = "e2e-web-user"
			js(w, 200, map[string]string{"token": tok})
			return
		case r.Method == "PUT" && strings.HasPrefix(p, "/-/user/org.couchdb.user:"):
			tok := fmt.Sprintf("fake-legacy-%d", time.Now().UnixNano())
			tokens[tok] = strings.TrimPrefix(p, "/-/user/org.couchdb.user:")
			js(w, 201, map[string]any{"ok": true, "token": tok})
			return
		}
		if user == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fake"`)
			js(w, 401, map[string]string{"error": "authentication required"})
			return
		}
		switch {
		case p == "/-/whoami":
			js(w, 200, map[string]string{"username": user})
		case r.Method == "DELETE" && strings.HasPrefix(p, "/-/user/token/"):
			delete(tokens, strings.TrimPrefix(p, "/-/user/token/"))
			js(w, 200, map[string]bool{"ok": true})
		case strings.HasPrefix(p, "/-/npm/v1/security/"):
			js(w, 200, map[string]any{})
		case strings.HasPrefix(p, "/-/package/") && strings.Contains(p, "/dist-tags"):
			js(w, 201, map[string]bool{"ok": true})
		case r.Method == "GET" && p == "/@acme/secret":
			js(w, 200, packument)
		case r.Method == "GET" && p == "/@acme/secret/-/secret-1.0.0.tgz":
			w.Write(tgz)
		case r.Method == "PUT" && !strings.HasPrefix(p, "/-/"):
			var doc struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(body, &doc) != nil || doc.Name == "" {
				js(w, 400, map[string]string{"error": "bad publish body"})
				return
			}
			if strings.HasPrefix(doc.Name, "otp-") && otp != "123456" {
				w.Header().Set("WWW-Authenticate", "OTP")
				js(w, 401, map[string]string{"error": "one-time password required"})
				return
			}
			published[doc.Name] = true
			js(w, 201, map[string]bool{"ok": true})
		case r.Method == "GET" && published[strings.TrimPrefix(p, "/")]:
			js(w, 200, map[string]any{"name": strings.TrimPrefix(p, "/"), "dist-tags": map[string]string{"latest": "1.0.0"}, "versions": map[string]any{}})
		default:
			js(w, 404, map[string]string{"error": "not found"})
		}
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func tarball(pkgJSON, index string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, data string }{{"package/package.json", pkgJSON}, {"package/index.js", index}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), ModTime: time.Unix(0, 0)})
		tw.Write([]byte(f.data))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
