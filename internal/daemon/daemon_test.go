package daemon

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRedact(t *testing.T) {
	if got := redact("/npm/-/user/token/npm_abc123"); got != "/npm/-/user/token/[redacted]" {
		t.Fatal(got)
	}
	if got := redact("/npm/react"); got != "/npm/react" {
		t.Fatal(got)
	}
}

func testConfig(t *testing.T) Config {
	t.Setenv("GH_TOKEN", "ghp_secret_must_not_appear")
	t.Setenv("PATH", "/usr/bin:/bin")
	return Config{Home: "/home/u/.cache/consona", Port: 48731}
}

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit(testConfig(t), "/home/u/.local/bin/consona")
	for _, want := range []string{"ExecStart=\"/home/u/.local/bin/consona\" daemon run\n", "Restart=always\n", "RestartSec=1\n",
		"Environment=\"CONSONA_HOME=/home/u/.cache/consona\"\n", "Environment=\"CONSONA_PORT=48731\"\n", "WantedBy=default.target\n",
		"StandardOutput=append:/home/u/.cache/consona/daemon.log\n"} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "ghp_") {
		t.Fatal("unit must not contain credentials")
	}
}

// plistDict decodes the top-level <dict> of a plist into key -> raw element.
func plistDict(t *testing.T, doc string) map[string]string {
	d := xml.NewDecoder(strings.NewReader(doc))
	out, key, depth := map[string]string{}, "", 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid plist XML: %v", err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 3 && e.Name.Local == "key" {
				var s string
				d.DecodeElement(&s, &e)
				key = s
				depth--
			} else if depth == 3 {
				var inner struct {
					Inner string `xml:",innerxml"`
				}
				d.DecodeElement(&inner, &e)
				out[key] = e.Name.Local + ":" + inner.Inner
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
	return out
}

func TestLaunchdPlist(t *testing.T) {
	p := launchdPlist(testConfig(t), "/usr/local/bin/consona")
	m := plistDict(t, p)
	for k, want := range map[string]string{
		"Label":            "string:dev.consona.consona",
		"RunAtLoad":        "true:",
		"KeepAlive":        "true:",
		"ThrottleInterval": "integer:1",
		"ProgramArguments": "array:<string>/usr/local/bin/consona</string><string>daemon</string><string>run</string>",
		"StandardOutPath":  "string:/home/u/.cache/consona/daemon.log",
	} {
		if m[k] != want {
			t.Fatalf("%s = %q, want %q\n%s", k, m[k], want, p)
		}
	}
	if !strings.Contains(m["EnvironmentVariables"], "<key>CONSONA_HOME</key><string>/home/u/.cache/consona</string>") || strings.Contains(p, "ghp_") {
		t.Fatalf("env: %s", m["EnvironmentVariables"])
	}
}

// The service gets paths that tell it where credentials live, never values.
func TestServiceEnvPassesCredentialLocationsOnly(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", "/home/u/.config/gh-work")
	t.Setenv("CONSONA_CONFIG_DIR", "/home/u/.config/consona")
	t.Setenv("CONSONA_KEYRING_SERVICE", "dev.consona.tokens.e2e-test")
	t.Setenv("npm_config_cache", "/tmp/npm-cache")
	t.Setenv("GH_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	t.Setenv("CONSONA_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	u := systemdUnit(testConfig(t), "/usr/bin/consona")
	if !strings.Contains(u, `Environment="CONSONA_KEYRING_SERVICE=dev.consona.tokens.e2e-test"`) || !strings.Contains(u, `Environment="npm_config_cache=/tmp/npm-cache"`) {
		t.Fatalf("missing keyring service / npm cache:\n%s", u)
	}
	if !strings.Contains(u, `Environment="GH_CONFIG_DIR=/home/u/.config/gh-work"`) || !strings.Contains(u, `Environment="CONSONA_CONFIG_DIR=/home/u/.config/consona"`) {
		t.Fatalf("missing config dirs:\n%s", u)
	}
	if strings.Contains(u, "ghp_") || strings.Contains(u, "GH_TOKEN") || strings.Contains(u, "CONSONA_TOKEN") {
		t.Fatalf("token in unit:\n%s", u)
	}
}

func TestWindowsTask(t *testing.T) {
	b := utf16File(windowsTask(testConfig(t), `C:\Users\u\consona.exe`))
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatal("schtasks needs a UTF-16LE BOM")
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	var task struct {
		Logon    *struct{} `xml:"Triggers>LogonTrigger"`
		Limit    string    `xml:"Settings>ExecutionTimeLimit"`
		Battery  string    `xml:"Settings>StopIfGoingOnBatteries"`
		Restart  string    `xml:"Settings>RestartOnFailure>Count"`
		Multiple string    `xml:"Settings>MultipleInstancesPolicy"`
		Command  string    `xml:"Actions>Exec>Command"`
		Args     string    `xml:"Actions>Exec>Arguments"`
	}
	d := xml.NewDecoder(strings.NewReader(string(utf16.Decode(u))))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Logon == nil || task.Limit != "PT0S" || task.Battery != "false" || task.Restart == "" || task.Multiple != "IgnoreNew" ||
		task.Command != `C:\Users\u\consona.exe` || task.Args != "daemon supervise" {
		t.Fatalf("%+v", task)
	}
}

// The supervisor restarts a crashing daemon with backoff and stops when
// the daemon exits cleanly.
func TestSuperviseLoop(t *testing.T) {
	runs, slept := 0, []time.Duration{}
	code := superviseLoop(func() error {
		runs++
		if runs < 4 {
			return errors.New("crashed")
		}
		return nil
	}, func(d time.Duration) { slept = append(slept, d) })
	if code != 0 || runs != 4 || len(slept) != 3 || slept[0] != time.Second || slept[2] != 4*time.Second {
		t.Fatalf("code %d runs %d slept %v", code, runs, slept)
	}
}
