// Package sshremote lets a device install packages registered on another
// machine it can reach over SSH. The client runs `consona remote-serve` on
// the remote through the system ssh binary (so the user's keys, agent,
// ~/.ssh/config and known_hosts apply; never a password prompt, never
// weaker host-key checking) and speaks a line-delimited JSON protocol on
// stdin/stdout. A remote only serves packages registered in its own local
// index, never arbitrary files, and every tarball is checked against the
// sha512 the remote announced.
package sshremote

import (
	"encoding/json"

	"github.com/tirthpatel002/consona-package-manager/internal/identity"
)

// Protocol is the wire protocol version.
const Protocol = "consona-remote/1"

// Request is one line from the client.
type Request struct {
	Op      string `json:"op"` // hello | list | resolve | fetch
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Summary is a registered package in a list answer.
type Summary struct {
	Name string `json:"name"`
}

// Package is a resolved, packed package on the remote.
type Package struct {
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Hash     string          `json:"hash"` // content hash (the remote's revision)
	File     string          `json:"file"`
	Manifest json.RawMessage `json:"manifest"`
	SHA1     string          `json:"sha1"`
	SHA512   string          `json:"sha512"` // base64
	Size     int64           `json:"size"`
}

// Response is one line from the server. A fetch response is followed by
// exactly Size raw bytes.
type Response struct {
	Protocol string           `json:"protocol"`
	OK       bool             `json:"ok"`
	Error    string           `json:"error,omitempty"`
	NotFound bool             `json:"not_found,omitempty"`
	Consona  string           `json:"consona,omitempty"` // server version
	Username string           `json:"username,omitempty"`
	Device   *identity.Device `json:"device,omitempty"`
	Packages []Summary        `json:"packages,omitempty"`
	Package  *Package         `json:"package,omitempty"`
	Size     int64            `json:"size,omitempty"`
	SHA512   string           `json:"sha512,omitempty"`
}
