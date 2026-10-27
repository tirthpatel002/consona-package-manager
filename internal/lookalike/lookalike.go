package lookalike

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// Status represents the existence status of a package on the public registry.
type Status string

const (
	StatusExists    Status = "exists"
	StatusNotExists Status = "not_exists"
	StatusUnknown   Status = "unknown"

	DefaultRegistry = "https://registry.npmjs.org"
	DefaultTimeout  = 3 * time.Second
	CacheTTL        = 24 * time.Hour
)

type cacheEntry struct {
	Status    Status    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

// Checker queries a public registry and caches lookalike results.
type Checker struct {
	RegistryURL string
	HomeDir     string
	Timeout     time.Duration
	HTTPClient  *http.Client
	Now         func() time.Time
	CachePath   string

	mu  sync.Mutex
	mem map[string]cacheEntry
}

var (
	defaultCheckerMu sync.Mutex
	defaultChecker   *Checker
)

// Default returns the process-wide default checker.
func Default() *Checker {
	defaultCheckerMu.Lock()
	defer defaultCheckerMu.Unlock()
	if defaultChecker == nil {
		defaultChecker = New()
	}
	return defaultChecker
}

// ResetDefault resets the process-wide default checker (useful in tests).
func ResetDefault() {
	defaultCheckerMu.Lock()
	defer defaultCheckerMu.Unlock()
	defaultChecker = nil
}

// New creates an initialized Checker.
func New() *Checker {
	return &Checker{
		mem: make(map[string]cacheEntry),
	}
}

// PublicRegistry returns the public registry URL, checking CONSONA_PUBLIC_REGISTRY.
func PublicRegistry() string {
	if v := strings.TrimSpace(os.Getenv("CONSONA_PUBLIC_REGISTRY")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultRegistry
}

// RegistryHost extracts the host part of a registry URL for user-facing messages.
func RegistryHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// EscapeName escapes a package name for npm registry URLs (@scope/pkg -> @scope%2fpkg).
func EscapeName(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), "%2F", "%2f")
}

func (c *Checker) registry() string {
	if c.RegistryURL != "" {
		return strings.TrimRight(c.RegistryURL, "/")
	}
	return PublicRegistry()
}

func (c *Checker) cacheFile() string {
	if c.CachePath != "" {
		return c.CachePath
	}
	home := c.HomeDir
	if home == "" {
		home = core.Home()
	}
	return filepath.Join(home, "lookalike.json")
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Checker) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: c.timeout()}
}

// Check queries whether name exists on the public registry.
// Cached answers (positive and negative) are reused within 24h.
// Unknown results (network errors, timeouts, 5xx) are not cached and fail open.
func (c *Checker) Check(ctx context.Context, name string) Status {
	name = strings.TrimSpace(name)
	if name == "" {
		return StatusUnknown
	}

	c.mu.Lock()
	if c.mem == nil {
		c.mem = make(map[string]cacheEntry)
	}
	now := c.now()
	if e, ok := c.mem[name]; ok && now.Sub(e.CheckedAt) < CacheTTL {
		c.mu.Unlock()
		return e.Status
	}
	c.loadDiskLocked()
	if e, ok := c.mem[name]; ok && now.Sub(e.CheckedAt) < CacheTTL {
		c.mu.Unlock()
		return e.Status
	}
	c.mu.Unlock()

	status := c.query(ctx, name)
	if status == StatusUnknown {
		return StatusUnknown
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.mem[name] = cacheEntry{Status: status, CheckedAt: now}
	c.saveDiskLocked()
	return status
}

func (c *Checker) query(ctx context.Context, name string) Status {
	escaped := EscapeName(name)
	target := c.registry() + "/" + escaped

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, target, nil)
	if err != nil {
		c.logSkip(name, err.Error())
		return StatusUnknown
	}
	req.Header = make(http.Header)
	req.Header.Set("User-Agent", "consona")

	resp, err := c.client().Do(req)
	if err != nil {
		c.logSkip(name, err.Error())
		return StatusUnknown
	}

	// Fallback to GET if HEAD is not supported by upstream or mock
	if resp.StatusCode == http.StatusMethodNotAllowed {
		_ = resp.Body.Close()
		req2, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
		if err == nil {
			req2.Header = make(http.Header)
			req2.Header.Set("User-Agent", "consona")
			resp, err = c.client().Do(req2)
		}
	}
	if err != nil {
		c.logSkip(name, err.Error())
		return StatusUnknown
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return StatusExists
	case http.StatusNotFound:
		return StatusNotExists
	default:
		c.logSkip(name, fmt.Sprintf("HTTP status %d", resp.StatusCode))
		return StatusUnknown
	}
}

func (c *Checker) logSkip(name, reason string) {
	logstore.LogQuiet(logstore.Entry{
		Event:     logstore.EvPublicLookalikeCheck,
		Level:     "debug",
		Ecosystem: "npm",
		Package:   name,
		Result:    "skipped",
		Message:   fmt.Sprintf("public lookalike check for %s skipped (%s)", name, reason),
	})
}

func (c *Checker) loadDiskLocked() {
	path := c.cacheFile()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var disk map[string]cacheEntry
	if err := json.Unmarshal(b, &disk); err != nil {
		return
	}
	now := c.now()
	for k, v := range disk {
		if now.Sub(v.CheckedAt) < CacheTTL {
			c.mem[k] = v
		}
	}
}

func (c *Checker) saveDiskLocked() {
	path := c.cacheFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	merged := make(map[string]cacheEntry)
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &merged)
	}
	now := c.now()
	cleaned := make(map[string]cacheEntry)
	for k, v := range merged {
		if now.Sub(v.CheckedAt) < CacheTTL {
			cleaned[k] = v
		}
	}
	for k, v := range c.mem {
		if now.Sub(v.CheckedAt) < CacheTTL {
			cleaned[k] = v
		}
	}

	data, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, fmt.Sprintf("lookalike.json.tmp.%d.%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
