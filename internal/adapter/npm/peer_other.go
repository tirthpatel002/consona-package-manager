//go:build !linux

package npm

import "net/http"

// peerClient is Linux-only; elsewhere the daemon uses its own view of npm
// config (environment, user and global npmrc).
func peerClient(*http.Request) (*client, bool, bool) { return nil, false, false }
