package npm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/builder/npmpack"
	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/devsync"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// Handler is npm's registry (setup points npm's `registry` here):
//   - routed private scopes are served from their GitHub repos and never
//     looked up publicly (a name it cannot serve is a clear 404);
//   - audit requests are forwarded upstream with private names removed
//     (audit.go);
//   - any other request goes to the user's previous ("upstream") registry,
//     unless it names a private scope (reads: 404, writes: 403):
//     anonymous reads, and unscoped package reads on registry.npmjs.org
//     (public by definition), are a 302, so that data never passes through
//     consona; anything needing the user's login (whoami, private or
//     authenticated-mirror packages, publish, login, dist-tag, ...) is
//     forwarded as-is with that login added (see authFor).
func (Adapter) Handler(e *core.Engine, baseURL string) http.Handler {
	return &handler{e: e, base: strings.TrimRight(baseURL, "/"), b: npmpack.Builder{}}
}

type handler struct {
	e    *core.Engine
	base string
	b    core.Builder

	upstreamURL string // tests only; default Upstream()
	noWarm      bool   // tests only: no background prebuild/prefetch

	warmTTL time.Duration // 0: defaultWarmTTL (see claimWarm)
	warmMu  sync.Mutex
	warmed  map[string]time.Time // package name -> when it was last warmed

	buildWait time.Duration // 0: BuildWait() (see awaitLatest); <0: never wait
}

// authFor is the Authorization to send upstream. npm holds a placeholder
// token for the daemon (see Placeholder), which stands for the user's own
// login for the upstream registry, looked up as npm would (see auth.go). A
// real token (from `npm login` made through consona) is passed through.
// Requests from another user's processes never get this user's login.
func (h *handler) authFor(r *http.Request, up string) (string, login, *client) {
	a := r.Header.Get("Authorization")
	if a != "" && a != "Bearer "+Placeholder {
		return a, login{}, nil
	}
	c, same := clientFor(r)
	if !same {
		return "", login{}, nil
	}
	lg := c.loginFor(up)
	return lg.header, lg, c
}

// redirect sends npm to the upstream registry for a read it can do itself.
func (h *handler) redirect(w http.ResponseWriter, r *http.Request, up string) {
	target := up + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
	logstore.LogQuiet(logstore.Entry{Event: logstore.EvRegistryRedirect, Level: "debug", Ecosystem: "npm",
		Package: pkgName(r.URL.Path), HTTPStatus: http.StatusFound, Result: "redirect", Message: r.Method + " " + r.URL.Path})
}

// unscopedPackage matches /name and /name/-/file.tgz.
func unscopedPackage(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if parts[0] == "" || strings.HasPrefix(parts[0], "@") || strings.HasPrefix(parts[0], "-") {
		return false
	}
	return len(parts) == 1 || (len(parts) == 3 && parts[1] == "-")
}

// proxy forwards the request as-is (method, body, npm headers such as
// npm-otp) to the upstream registry with auth, and relays the answer.
func (h *handler) proxy(w http.ResponseWriter, r *http.Request, up, auth string, onOK func()) {
	target, err := url.Parse(up)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "bad upstream registry "+up)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del("Authorization")
			if auth != "" {
				pr.Out.Header.Set("Authorization", auth)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if onOK != nil && resp.StatusCode/100 == 2 {
				onOK()
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			jsonErr(w, http.StatusBadGateway, "consona: upstream registry "+up+": "+err.Error())
		},
	}
	rp.ServeHTTP(w, r)
}

// logout handles `npm logout` while npm holds the placeholder: the user's
// real upstream token is revoked upstream and, if it is written in their
// user npmrc, removed from it (npm itself removes the placeholder). Routing
// is unaffected and a later `npm login` stores a new token for the daemon.
func (h *handler) logout(w http.ResponseWriter, r *http.Request, up string, lg login, c *client) {
	if lg.token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		return
	}
	r.URL.Path = "/-/user/token/" + lg.token
	r.URL.RawPath = ""
	h.proxy(w, r, up, lg.header, func() {
		if lg.src.file == c.userFile() {
			go forgetLogin(lg.src)
		}
	})
}

func (h *handler) upstream() string {
	if h.upstreamURL != "" {
		return h.upstreamURL
	}
	return Upstream()
}

// mentionsScope reports which routed scope, if any, a request names
// anywhere in its path or query (e.g. /-/package/@o%2fx/dist-tags).
func mentionsScope(r *http.Request, scopes map[string]bool) string {
	q, _ := url.QueryUnescape(r.URL.RawQuery)
	s := strings.ToLower(r.URL.Path + "?" + q)
	for o := range scopes {
		if strings.Contains(s, "@"+o+"/") {
			return o
		}
	}
	return ""
}

// parse splits "/@scope/name[/-/file.tgz]" (path already %2f-decoded).
func parse(p string) (ref core.Ref, file string, ok bool) {
	p = strings.TrimPrefix(p, "/")
	if !strings.HasPrefix(p, "@") {
		return ref, "", false
	}
	parts := strings.Split(p[1:], "/")
	switch {
	case len(parts) == 2:
	case len(parts) == 4 && parts[2] == "-" && strings.HasSuffix(parts[3], ".tgz"):
		file = parts[3]
	default:
		return ref, "", false
	}
	if parts[0] == "" || parts[1] == "" {
		return ref, "", false
	}
	return core.Ref{Owner: parts[0], Name: parts[1]}, file, true
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/-/warm" && r.Method == http.MethodPost:
		var names []string
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&names)
		h.warmRefs(names)
		w.WriteHeader(http.StatusAccepted)
		return
	case r.Method == http.MethodPost && r.URL.Path == bulkPath:
		h.bulkAudit(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == quickPath:
		h.quickAudit(w, r)
		return
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	scopes := privateScopes()
	ref, file, ok := parse(r.URL.Path)
	ok = ok && scopes[strings.ToLower(ref.Owner)]
	switch o := mentionsScope(r, scopes); {
	case ok && read:
		// served below
	case o != "" && !read:
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("consona: @%s is a private scope served from GitHub; it is never published or changed on a public registry (release by pushing a git tag)", o))
		return
	case o != "":
		jsonErr(w, http.StatusNotFound, fmt.Sprintf("consona: @%s is a private scope; this request is not sent to the public registry", o))
		return
	default:
		up := h.upstream()
		// Unscoped packages on the default registry are public: npm is sent
		// there directly whatever its login, so the npm process is not looked
		// up (keeps plain installs free of the peer lookup).
		if read && up+"/" == DefaultUpstream && unscopedPackage(r.URL.Path) {
			h.redirect(w, r, up)
			return
		}
		auth, lg, c := h.authFor(r, up)
		if r.Method == http.MethodDelete && r.URL.Path == "/-/user/token/"+Placeholder {
			h.logout(w, r, up, lg, c)
			return
		}
		if read && auth == "" {
			h.redirect(w, r, up)
			return
		}
		start := time.Now()
		h.proxy(w, r, up, auth, nil)
		logstore.LogQuiet(logstore.Entry{Event: logstore.EvAuthForward, Level: "info", Ecosystem: "npm", Package: pkgName(r.URL.Path),
			Result: "forwarded", DurationMS: time.Since(start).Milliseconds(), Message: fmt.Sprintf("%s %s forwarded to the upstream registry (login attached: %v)", r.Method, r.URL.Path, auth != "")})
		return
	}
	name := "@" + ref.Owner + "/" + ref.Name
	start := time.Now()
	pkgs, err := h.e.Packages(r.Context(), h.b, ref, name)
	if err != nil {
		if p := h.e.FindCached(h.b, ref, file); p != "" { // e.g. remote offline: a built tarball is still good
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, p)
			h.trackDevProject(r, name, ref)
			return
		}
		h.fail(w, ref, name, err)
		return
	}
	if file != "" {
		for _, p := range pkgs {
			if npmpack.FileName(name, p.Version) != file {
				continue
			}
			h.checkName(ref, name, p)
			bl := p.Built
			if bl == nil {
				if bl, err = h.e.Build(r.Context(), h.b, ref, name, p); err != nil {
					h.fail(w, ref, name, err)
					return
				}
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, bl.Path)
			logstore.LogQuiet(logstore.Entry{Event: logstore.EvInstallFetch, Level: "info", Ecosystem: "npm", Package: name, Owner: ref.Owner,
				Repo: ref.Name, Version: p.Version, CommitSHA: p.Revision.Commit, Result: "ok", DurationMS: time.Since(start).Milliseconds()})
			h.trackDevProject(r, name, ref)
			return
		}
		// A version the source no longer lists (a local folder changed
		// since the lockfile was written) is still served if it was built.
		if p := h.e.FindCached(h.b, ref, file); p != "" {
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, p)
			h.trackDevProject(r, name, ref)
			return
		}
		jsonErr(w, http.StatusNotFound, name+": no such tarball "+file)
		return
	}
	if !h.noWarm {
		go h.warmDeps(name, pkgs, scopes)
	}
	h.checkName(ref, name, latest(pkgs))
	pkgs = h.awaitLatest(r.Context(), ref, name, pkgs)
	body, err := Packument(name, h.base, pkgs)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	logstore.LogQuiet(logstore.Entry{Event: logstore.EvInstallResolve, Level: "info", Ecosystem: "npm", Package: name, Owner: ref.Owner,
		Repo: ref.Name, Result: "ok", DurationMS: time.Since(start).Milliseconds(), Fields: map[string]any{"versions": len(pkgs)}})
}

// pkgName extracts the package name from a registry path ("" if none).
func pkgName(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || strings.HasPrefix(parts[0], "-") {
		return ""
	}
	if strings.HasPrefix(parts[0], "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// fail answers with one clear message. Not-found is a 404; a credential
// problem a 403 and a build failure a 422, so npm stops at once instead of
// retrying (npm retries 5xx and 429). Credential errors name every source
// tried and how to fix it; they also go to the daemon log and the store.
func (h *handler) fail(w http.ResponseWriter, ref core.Ref, name string, err error) {
	var msg string
	code := http.StatusBadGateway
	ecode := "upstream"
	var coder interface{ Code() string }
	if errors.As(err, &coder) {
		ecode = coder.Code()
	}
	switch {
	case errors.Is(err, core.ErrAccess):
		code = http.StatusForbidden
		if errors.Is(err, core.ErrNotFound) {
			code = http.StatusNotFound
		}
		msg = fmt.Sprintf("%s is private (scope @%s is routed to consona) but %v", name, ref.Owner, err)
	case errors.Is(err, core.ErrNotFound):
		code, ecode = http.StatusNotFound, "not-found"
		msg = fmt.Sprintf("%s is private (scope @%s is routed to consona) but %s was not found or is not accessible with your GitHub credentials. Check the name or request access; `consona token test` shows what each credential can see.", name, ref.Owner, h.e.Source.Describe(ref))
	case errors.Is(err, core.ErrBuild):
		code, ecode = http.StatusUnprocessableEntity, "build-failed"
		msg = err.Error()
	default:
		msg = fmt.Sprintf("%s: %v", name, err)
	}
	h.e.ReportError(msg)
	logstore.Log(logstore.Entry{Event: logstore.EvError, Level: "error", Ecosystem: "npm", Package: name, Owner: ref.Owner, Repo: ref.Name,
		Result: "fail", HTTPStatus: code, ErrorCode: ecode, Message: msg})
	jsonErr(w, code, msg)
}

// Packument renders the npm registry document for a package. Versions not
// built yet carry their declared manifest and tarball URL without hashes
// (the handler first waits a bounded time for the latest version's build,
// see awaitLatest, so that one normally has them).
func Packument(name, base string, pkgs []*core.Version) ([]byte, error) {
	versions := map[string]any{}
	var list []string
	for _, p := range pkgs {
		raw := p.Info.Manifest
		if p.Built != nil {
			raw = p.Built.Artifact.Manifest
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		m["name"], m["version"], m["_id"] = name, p.Version, name+"@"+p.Version
		if p.Revision.Commit != "" {
			m["gitHead"] = p.Revision.Commit
		}
		dist := map[string]any{"tarball": base + "/" + name + "/-/" + npmpack.FileName(name, p.Version)}
		if p.Built != nil {
			dist["shasum"] = p.Built.Artifact.SHA1
			dist["integrity"] = "sha512-" + p.Built.Artifact.SHA512
		}
		m["dist"] = dist
		versions[p.Version] = m
		list = append(list, p.Version)
	}
	return json.Marshal(map[string]any{
		"_id":       name,
		"name":      name,
		"dist-tags": map[string]string{"latest": core.Latest(list)},
		"versions":  versions,
	})
}

func (h *handler) trackDevProject(r *http.Request, name string, ref core.Ref) {
	if !h.e.Live(ref) {
		return
	}
	c, same := clientFor(r)
	if !same || c == nil {
		return
	}
	projectDir := ""
	if p := c.getenv("npm_config_local_prefix"); p != "" {
		projectDir = p
	} else {
		projectDir = c.cwd
	}
	if projectDir == "" {
		return
	}
	_ = devsync.TrackProject(core.Home(), name, projectDir)
}
