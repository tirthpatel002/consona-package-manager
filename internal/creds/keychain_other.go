//go:build !darwin

package creds

// storeUsable: other platforms report an unusable store from the call
// itself, without any UI.
func storeUsable() error { return nil }
