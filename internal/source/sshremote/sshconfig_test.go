package sshremote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSSHConfig(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "conf.d"), 0o700)
	os.WriteFile(filepath.Join(dir, "conf.d", "work.conf"), []byte("Host build-box\n  HostName 10.0.0.5\n  User ci\n"), 0o600)
	cfg := filepath.Join(dir, "config")
	os.WriteFile(cfg, []byte(`# comment
Include conf.d/*.conf
Host *
  ServerAliveInterval 30
Host vm vm-alias
  HostName 130.0.0.1
  User ubuntu
  HostName ignored-second
Host github.com gh
  HostName github.com
  User git
Host "quoted"
  HostName q.example
Host web-* !web-bad
  User x
Host gitlab.example.com
  User git
Match host foo
  User nobody
Host=mac
  HostName mac.local
Host vm
  User other
`), 0o600)
	got := ParseSSHConfig(cfg)
	want := []HostEntry{
		{Alias: "build-box", HostName: "10.0.0.5", User: "ci"},
		{Alias: "vm", HostName: "130.0.0.1", User: "ubuntu"},
		{Alias: "vm-alias", HostName: "130.0.0.1", User: "ubuntu"},
		{Alias: "quoted", HostName: "q.example"},
		{Alias: "mac", HostName: "mac.local"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if ParseSSHConfig(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing file")
	}
}
