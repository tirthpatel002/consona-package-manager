package creds

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKeychainUsableRefusesOtherHome(t *testing.T) {
	real := t.TempDir()
	if err := keychainUsable(t.TempDir(), real); err == nil {
		t.Fatal("a different HOME must be refused before any security call")
	}
	if err := keychainUsable(real, real); err == nil {
		t.Fatal("no login keychain must be refused")
	}
	kc := filepath.Join(real, "Library", "Keychains")
	if err := os.MkdirAll(kc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kc, "login.keychain-db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keychainUsable(real, real); err != nil {
		t.Fatalf("real HOME with a login keychain: %v", err)
	}
}

func TestSystemStoreTempHomeNoSecurityCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var ns *NoStoreError
	if err := (SystemStore{}).Set("x", "v"); !errors.As(err, &ns) {
		t.Fatalf("Set with a temp HOME: want NoStoreError, got %v", err)
	}
	if _, err := (SystemStore{}).Get("x"); !errors.As(err, &ns) {
		t.Fatalf("Get with a temp HOME: want NoStoreError, got %v", err)
	}
}
