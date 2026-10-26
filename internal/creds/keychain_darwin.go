package creds

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// storeUsable checks, without calling the security tool, that the macOS
// Keychain can be used non-interactively. With HOME pointing anywhere but
// the user's real home there is no login keychain, and `security
// add-generic-password` would make macOS show a "Keychain Not Found ...
// Reset To Defaults" dialog instead of failing; so consona refuses first.
func storeUsable() error {
	return keychainUsable(os.Getenv("HOME"), realHome())
}

func realHome() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

func keychainUsable(home, real string) error {
	if home == "" || real == "" {
		return fmt.Errorf("cannot tell the user's home directory (HOME=%q)", home)
	}
	if h, r := cleanReal(home), cleanReal(real); h != r {
		return fmt.Errorf("HOME is %s, not the user's home %s: consona uses the Keychain only with the real HOME (a different HOME has no login keychain and macOS would show a dialog)", home, real)
	}
	for _, f := range []string{"login.keychain-db", "login.keychain"} {
		if _, err := os.Stat(filepath.Join(real, "Library", "Keychains", f)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no login keychain in %s", filepath.Join(real, "Library", "Keychains"))
}

func cleanReal(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}
