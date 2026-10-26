// Package daemon runs the loopback HTTP server that hosts every adapter's
// protocol handler, and starts it on demand from the CLI.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
	"github.com/tirthpatel002/consona-package-manager/internal/logstore"
)

// Config locates the daemon.
type Config struct {
	Port int
	Home string // cache + log directory
}

// Load reads CONSONA_PORT / CONSONA_HOME with defaults 48731 and ~/.cache/consona.
func Load() Config {
	c := Config{Port: 48731}
	if p, err := strconv.Atoi(os.Getenv("CONSONA_PORT")); err == nil && p > 0 {
		c.Port = p
	}
	c.Home = core.Home()
	return c
}

func (c Config) Addr() string    { return "127.0.0.1:" + strconv.Itoa(c.Port) }
func (c Config) BaseURL() string { return "http://" + c.Addr() }
func (c Config) LogPath() string { return filepath.Join(c.Home, "daemon.log") }

// identity changes when the binary or home changes, so a stale daemon is replaced.
func (c Config) identity() string {
	exe, _ := os.Executable()
	st, _ := os.Stat(exe)
	var sz, mt int64
	if st != nil {
		sz, mt = st.Size(), st.ModTime().UnixNano()
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", exe, sz, mt, c.Home)))
	return hex.EncodeToString(h[:8])
}

// OpenLogs opens the structured log store in the consona home and makes
// it the process default (echoing events to the human log if echo).
// Callers must Close the result.
func OpenLogs(c Config, echo bool) *logstore.Async {
	s, err := logstore.Open(logstore.Dir(c.Home))
	if err != nil {
		log.Printf("consona: log store unavailable: %v", err)
		return nil
	}
	a := logstore.NewAsync(s)
	logstore.SetDefault(a, echo)
	return a
}

// Serve runs the daemon in the foreground until stopped.
func Serve(c Config, src core.Source) error {
	e := core.NewEngine(src, filepath.Join(c.Home, "cache"))
	ln, err := net.Listen("tcp", c.Addr())
	if err != nil {
		return err
	}
	if logs := OpenLogs(c, true); logs != nil {
		defer func() {
			logstore.SetDefault(nil, false)
			_ = logs.Close()
		}()
		prune := func() {
			if n, err := logs.S.Prune(logstore.DefaultRetention()); err == nil && n > 0 {
				log.Printf("consona: log store pruned %d old entries", n)
			}
		}
		go func() {
			prune()
			for range time.Tick(6 * time.Hour) {
				prune()
			}
		}()
	}
	logstore.Log(logstore.Entry{Event: logstore.EvDaemonStart, Level: "info", Result: "ok", Message: fmt.Sprintf("listening on %s (home %s)", c.Addr(), c.Home)})
	defer logstore.Log(logstore.Entry{Event: logstore.EvDaemonStop, Level: "info", Result: "ok", Message: "stopped"})
	mux := http.NewServeMux()
	for _, a := range core.Adapters() {
		prefix := "/" + a.Name()
		mux.Handle(prefix+"/", http.StripPrefix(prefix, a.Handler(e, c.BaseURL()+prefix)))
	}
	srv := &http.Server{Handler: logRequests(mux)}
	mux.HandleFunc("/-/consona/ping", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": c.identity()})
	})
	mux.HandleFunc("/-/consona/errors", func(w http.ResponseWriter, r *http.Request) {
		ns, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		_ = json.NewEncoder(w).Encode(e.ErrorsSince(time.Unix(0, ns)))
	})
	mux.HandleFunc("/-/consona/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		go func() { time.Sleep(50 * time.Millisecond); _ = srv.Close() }()
	})
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	go func() {
		if _, ok := <-sigc; ok {
			_ = srv.Close() // flush the log store on a service-manager stop too
		}
	}()
	log.Printf("consona daemon listening on %s (home %s)", c.Addr(), c.Home)
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{w, 200}
		h.ServeHTTP(sw, r)
		if r.URL.Path != "/-/consona/ping" {
			log.Printf("%s %s %d %s", r.Method, redact(r.URL.Path), sw.code, time.Since(t).Round(time.Millisecond))
		}
	})
}

// redact hides credentials that npm puts in URL paths (npm logout sends
// DELETE /-/user/token/<token>).
func redact(p string) string {
	if i := strings.Index(p, "/-/user/token/"); i >= 0 {
		return p[:i] + "/-/user/token/[redacted]"
	}
	return p
}

var client = &http.Client{Timeout: 2 * time.Second}

func ping(c Config) (string, bool) {
	resp, err := client.Get(c.BaseURL() + "/-/consona/ping")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var v struct{ ID string }
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return "", true
	}
	return v.ID, true
}

// Stop asks a running daemon to exit and waits briefly for the port to free.
// A daemon run by the login service is stopped through the service manager
// (otherwise it would be restarted at once).
func Stop(c Config) bool {
	if _, up := ping(c); !up {
		return false
	}
	if AutostartInstalled(c) {
		_ = serviceStop(c)
		return waitDown(c)
	}
	return stopDirect(c)
}

func stopDirect(c Config) bool {
	resp, err := client.Post(c.BaseURL()+"/-/consona/stop", "", nil)
	if err == nil {
		resp.Body.Close()
	}
	return waitDown(c)
}

func waitDown(c Config) bool {
	for i := 0; i < 250; i++ {
		if _, up := ping(c); !up {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// waitUp waits for any daemon to answer on the port.
func waitUp(c Config, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, up := ping(c); up {
			return true
		}
	}
	return false
}

// Ensure makes sure a daemon matching this binary and home is running,
// starting one in the background if needed.
func Ensure(c Config) error {
	id, up := ping(c)
	if up && id == c.identity() {
		return nil
	}
	if AutostartInstalled(c) {
		// The login service owns the daemon: (re)start it there rather than
		// racing it with a second process.
		if up {
			_ = serviceStop(c)
			waitDown(c)
		}
		if err := serviceStart(c); err != nil {
			return fmt.Errorf("cannot start the consona service: %v", err)
		}
		if !waitUp(c, 10*time.Second) {
			return fmt.Errorf("consona service did not start; see %s", c.LogPath())
		}
		return nil
	}
	if up {
		stopDirect(c)
	}
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(c.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if id, up := ping(c); up && id == c.identity() {
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("consona daemon failed to start; see %s", c.LogPath())
		case <-ctx.Done():
			return fmt.Errorf("consona daemon did not start in time; see %s", c.LogPath())
		case <-time.After(15 * time.Millisecond):
		}
	}
}

// ErrorsSince fetches errors the daemon reported after t.
func ErrorsSince(c Config, t time.Time) []string {
	resp, err := client.Get(fmt.Sprintf("%s/-/consona/errors?since=%d", c.BaseURL(), t.UnixNano()))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out []string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}
