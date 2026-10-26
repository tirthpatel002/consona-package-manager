// Package config is the one place that knows where consona keeps its
// per-user configuration and how it writes it. Layout of Dir():
//
//	identity.json      username + this device's id          (consona init)
//	local-index.json   folders registered on this device    (consona register)
//	remotes.json       ssh remotes added explicitly         (consona remote add)
//	tokens.json        named-token metadata, never values   (consona token add)
//	logs/events.db     structured event log (SQLite)        (consona logs)
//	logs/jsonl/        JSON-lines fallback of the event log
//
// Dir is $CONSONA_CONFIG_DIR, else <user config dir>/consona:
// ~/.config/consona (Linux), ~/Library/Application Support/consona
// (macOS), %AppData%\consona (Windows). The directory and logs/ are 0700,
// every file 0600, and every file is replaced atomically (synced temp
// file + rename), so a crash never leaves a half-written file. Token
// values live only in the OS secure store (Keychain, Secret Service,
// Credential Manager). Caches, builds, daemon.log and ssh state live in
// consona home (core.Home), not here.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dir is consona's config directory (see the package doc).
func Dir() string {
	if d := os.Getenv("CONSONA_CONFIG_DIR"); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".config")
	}
	return filepath.Join(d, "consona")
}

// Path is a file in the config directory.
func Path(name string) string { return filepath.Join(Dir(), name) }

// LogDir is the event log directory: $CONSONA_LOG_DIR, else Dir()/logs.
func LogDir() string {
	if d := os.Getenv("CONSONA_LOG_DIR"); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	return filepath.Join(Dir(), "logs")
}

// EnsureDir creates dir (and parents) and makes dir itself 0700.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(dir, 0o700)
}

// WriteFileAtomic writes data to path via a synced temp file and rename,
// creating the directory 0700. The file gets perm (normally 0600).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// HomeRel shortens a path under the user's home to ~/... for display.
func HomeRel(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && h != "/" {
		if p == h {
			return "~"
		}
		if strings.HasPrefix(p, h+string(filepath.Separator)) {
			return "~" + p[len(h):]
		}
	}
	return p
}

// PrintKV prints "key: value" lines with the values aligned. It is the
// one style for consona's info-type output (whoami, logs info, token list
// footer).
func PrintKV(w io.Writer, kv ...[2]string) {
	width := 0
	for _, p := range kv {
		if len(p[0]) > width {
			width = len(p[0])
		}
	}
	for _, p := range kv {
		fmt.Fprintf(w, "%-*s %s\n", width+1, p[0]+":", p[1])
	}
}
