package npm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// npm sends audits (every package name + version in the tree, private ones
// included) to its registry, which setup makes the daemon. The daemon drops
// routed private names and forwards the rest to the upstream registry, so
// advisories stay npm's real data and private names never leave the
// machine. npm uses the bulk endpoint and falls back to the quick one.
const (
	bulkPath  = "/-/npm/v1/security/advisories/bulk"
	quickPath = "/-/npm/v1/security/audits/quick"
)

var auditClient = &http.Client{Timeout: 60 * time.Second}

// FilterBulk removes routed private names from an npm bulk audit payload.
func FilterBulk(payload map[string]json.RawMessage, scopes map[string]bool) (withheld int) {
	for name := range payload {
		if _, ok := privateRef(name, scopes); ok {
			delete(payload, name)
			withheld++
		}
	}
	return withheld
}

// FilterQuick removes routed private names from a quick-audit tree
// ({name, requires, dependencies: {name: {requires, dependencies}}}).
func FilterQuick(node map[string]any, scopes map[string]bool) (withheld int) {
	if n, _ := node["name"].(string); n != "" {
		if _, ok := privateRef(n, scopes); ok {
			node["name"] = "consona-project"
		}
	}
	if req, ok := node["requires"].(map[string]any); ok {
		for n := range req {
			if _, ok := privateRef(n, scopes); ok {
				delete(req, n)
			}
		}
	}
	if deps, ok := node["dependencies"].(map[string]any); ok {
		for n, d := range deps {
			if _, ok := privateRef(n, scopes); ok {
				delete(deps, n)
				withheld++
			} else if m, ok := d.(map[string]any); ok {
				withheld += FilterQuick(m, scopes)
			}
		}
	}
	return withheld
}

func readJSON(r *http.Request, v any) error {
	var body io.Reader = io.LimitReader(r.Body, 64<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		body = gz
	}
	return json.NewDecoder(body).Decode(v)
}

func (h *handler) bulkAudit(w http.ResponseWriter, r *http.Request) {
	payload := map[string]json.RawMessage{}
	if err := readJSON(r, &payload); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad audit body")
		return
	}
	withheld := FilterBulk(payload, RoutedScopes())
	logstore.Log(logstore.Entry{Event: logstore.EvAuditFilter, Level: "info", Ecosystem: "npm", Result: "ok",
		Message: fmt.Sprintf("audit: forwarding %d public package names, withheld %d private", len(payload), withheld)})
	if len(payload) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	h.forward(w, r, bulkPath, payload)
}

func (h *handler) quickAudit(w http.ResponseWriter, r *http.Request) {
	tree := map[string]any{}
	if err := readJSON(r, &tree); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad audit body")
		return
	}
	logstore.Log(logstore.Entry{Event: logstore.EvAuditFilter, Level: "info", Ecosystem: "npm", Result: "ok",
		Message: fmt.Sprintf("audit (quick): withheld %d private", FilterQuick(tree, RoutedScopes()))})
	h.forward(w, r, quickPath, tree)
}

// forward POSTs the filtered payload upstream and relays the answer.
func (h *handler) forward(w http.ResponseWriter, r *http.Request, path string, payload any) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.upstream()+path, bytes.NewReader(data))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a, _, _ := h.authFor(r, h.upstream()); a != "" {
		req.Header.Set("Authorization", a)
	}
	resp, err := auditClient.Do(req)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "audit upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
