package core

import (
	"os"
	"path/filepath"
)

// Home is consona's state, cache and log directory: $CONSONA_HOME, else
// <user cache dir>/consona.
func Home() string {
	if h := os.Getenv("CONSONA_HOME"); h != "" {
		return h
	}
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "consona")
}
