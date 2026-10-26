// Package creds finds the GitHub credential to use for an owner. It knows
// every source consona accepts (per-owner env vars, named tokens kept in
// the OS secure store, CONSONA_TOKEN, GH_TOKEN, GITHUB_TOKEN, the gh
// login) and the order they are tried in; it never writes a token value
// anywhere except the OS secure store.
package creds

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/zalando/go-keyring"
)

// Service is the secure-store service name token values are saved under
// (one entry per token name). CONSONA_KEYRING_SERVICE overrides it, so
// tests can use a clearly named throwaway service in the real keychain.
var Service = serviceName()

func serviceName() string {
	if s := os.Getenv("CONSONA_KEYRING_SERVICE"); s != "" {
		return s
	}
	return "dev.consona.tokens"
}

// ErrNoSecret means the secure store has no value for a token name.
var ErrNoSecret = errors.New("token value not found in the OS secure store")

// Store keeps token values. The real one is the OS secure store; tests
// use a map.
type Store interface {
	Get(name string) (string, error)
	Set(name, secret string) error
	Delete(name string) error
}

// NoStoreError means there is no usable OS secure store (typically a
// headless Linux machine without a Secret Service). consona never falls
// back to a plaintext file.
type NoStoreError struct{ Err error }

func (e *NoStoreError) Error() string {
	where := map[string]string{
		"darwin":  "the macOS Keychain",
		"windows": "the Windows Credential Manager",
	}[runtime.GOOS]
	if where == "" {
		where = "a Secret Service (gnome-keyring, KeePassXC, ...) on the D-Bus session bus"
	}
	return fmt.Sprintf("no OS secure store available (%s needed: %v).\n"+
		"  consona keeps named tokens only in the OS secure store and never in a plaintext file.\n"+
		"  Fix: start a Secret Service (e.g. `dbus-run-session -- sh -c 'gnome-keyring-daemon --unlock --components=secrets; ...'`), or skip named tokens:\n"+
		"  set CONSONA_TOKEN (all owners), CONSONA_TOKEN_<OWNER> (one owner) or GH_TOKEN in the environment, or run `gh auth login`.",
		where, e.Err)
}

func (e *NoStoreError) Unwrap() error { return e.Err }

// SystemStore is the OS secure store: macOS Keychain, Windows Credential
// Manager, or the Linux Secret Service (via github.com/zalando/go-keyring).
type SystemStore struct{}

func (SystemStore) Get(name string) (string, error) {
	v, err := keyring.Get(Service, name)
	return v, storeErr(err)
}

func (SystemStore) Set(name, secret string) error {
	return storeErr(keyring.Set(Service, name, secret))
}

func (SystemStore) Delete(name string) error {
	return storeErr(keyring.Delete(Service, name))
}

func storeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNoSecret
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return err
	}
	return &NoStoreError{Err: err}
}

// MapStore is an in-memory Store for tests.
type MapStore struct {
	M   map[string]string
	Err error // if set, every call fails with &NoStoreError{Err}
}

func (m *MapStore) Get(name string) (string, error) {
	if m.Err != nil {
		return "", &NoStoreError{m.Err}
	}
	v, ok := m.M[name]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

func (m *MapStore) Set(name, secret string) error {
	if m.Err != nil {
		return &NoStoreError{m.Err}
	}
	if m.M == nil {
		m.M = map[string]string{}
	}
	m.M[name] = secret
	return nil
}

func (m *MapStore) Delete(name string) error {
	if m.Err != nil {
		return &NoStoreError{m.Err}
	}
	if _, ok := m.M[name]; !ok {
		return ErrNoSecret
	}
	delete(m.M, name)
	return nil
}
