package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// The daemon can be registered with the OS so it starts at login and is
// restarted if it crashes: a systemd user unit on Linux, a launchd
// LaunchAgent on macOS, a logon task on Windows (untested) running
// `consona daemon supervise`, which restarts the daemon if it crashes.
// One service per port, so test instances do not collide.

func (c Config) serviceName() string {
	if c.Port == 48731 {
		return "consona"
	}
	return "consona-" + strconv.Itoa(c.Port)
}

func (c Config) launchdLabel() string { return "dev.consona." + c.serviceName() }

func home() string { h, _ := os.UserHomeDir(); return h }

func (c Config) servicePath() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home(), "Library", "LaunchAgents", c.launchdLabel()+".plist")
	case "windows":
		return filepath.Join(c.Home, c.serviceName()+".task.xml") // the task definition given to Task Scheduler
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home(), ".config")
	}
	return filepath.Join(cfg, "systemd", "user", c.serviceName()+".service")
}

// AutostartInstalled reports whether this daemon is managed by the OS.
func AutostartInstalled(c Config) bool {
	_, err := os.Stat(c.servicePath())
	return err == nil
}

// serviceEnv is what the daemon needs to behave like the CLI that set it
// up: its home/port, the npm config it serves, and a PATH with npm/node
// (used for builds), and where to find credentials: the gh config dir and
// consona's config dir (paths only). Never credentials themselves: tokens
// are read at request time from the OS secure store or gh.
func (c Config) serviceEnv() [][2]string {
	env := [][2]string{{"CONSONA_HOME", c.Home}, {"CONSONA_PORT", strconv.Itoa(c.Port)}, {"PATH", os.Getenv("PATH")}}
	for _, k := range []string{"CONSONA_CONFIG_DIR", "GH_CONFIG_DIR", "CONSONA_LOG_DIR", "CONSONA_LOG_BACKEND", "CONSONA_LOG_MAX_AGE", "CONSONA_LOG_MAX_SIZE_MB",
		"CONSONA_SSH_CONFIG", "CONSONA_SSH_AUTODETECT", "CONSONA_SSH_CONTROL", "CONSONA_REMOTE_CMD"} {
		if v := os.Getenv(k); v != "" {
			if strings.HasSuffix(k, "_DIR") || k == "CONSONA_SSH_CONFIG" {
				if abs, err := filepath.Abs(v); err == nil {
					v = abs
				}
			}
			env = append(env, [2]string{k, v})
		}
	}
	for _, k := range []string{"npm_config_userconfig", "NPM_CONFIG_USERCONFIG"} {
		if v := os.Getenv(k); v != "" {
			if abs, err := filepath.Abs(v); err == nil {
				v = abs
			}
			env = append(env, [2]string{"npm_config_userconfig", v})
			break
		}
	}
	return env
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallAutostart registers the daemon with the OS service manager and
// starts it there. Returns the file written.
func InstallAutostart(c Config) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if e, err := filepath.EvalSymlinks(exe); err == nil {
		exe = e
	}
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return "", err
	}
	stopDirect(c) // a daemon started by the CLI would hold the port
	path := c.servicePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	var content string
	switch runtime.GOOS {
	case "darwin":
		content = launchdPlist(c, exe)
	case "windows":
		content = string(utf16File(windowsTask(c, exe)))
	default:
		content = systemdUnit(c, exe)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		_ = run("launchctl", "bootout", c.launchdTarget())
		err = run("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path)
	case "windows":
		err = run("schtasks", "/Create", "/F", "/TN", c.serviceName(), "/XML", path)
		if err == nil {
			err = run("schtasks", "/Run", "/TN", c.serviceName())
		}
	default:
		if err = run("systemctl", "--user", "daemon-reload"); err == nil {
			if err = run("systemctl", "--user", "enable", c.serviceName()+".service"); err == nil {
				err = run("systemctl", "--user", "restart", c.serviceName()+".service") // picks up a new binary on re-setup
			}
		}
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if !waitUp(c, 10*time.Second) {
		return path, fmt.Errorf("consona service registered but not answering; see %s", c.LogPath())
	}
	return path, nil
}

// RemoveAutostart stops and unregisters the service (no-op if absent).
func RemoveAutostart(c Config) error {
	path := c.servicePath()
	if !AutostartInstalled(c) {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		_ = run("launchctl", "bootout", c.launchdTarget())
	case "windows":
		_ = run("schtasks", "/End", "/TN", c.serviceName())
		_ = run("schtasks", "/Delete", "/F", "/TN", c.serviceName())
	default:
		_ = run("systemctl", "--user", "disable", "--now", c.serviceName()+".service")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		_ = run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (c Config) launchdTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + c.launchdLabel()
}

func serviceStart(c Config) error {
	switch runtime.GOOS {
	case "darwin":
		_ = run("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), c.servicePath())
		return run("launchctl", "kickstart", c.launchdTarget())
	case "windows":
		return run("schtasks", "/Run", "/TN", c.serviceName())
	}
	return run("systemctl", "--user", "start", c.serviceName()+".service")
}

func serviceStop(c Config) error {
	switch runtime.GOOS {
	case "darwin":
		return run("launchctl", "bootout", c.launchdTarget()) // KeepAlive would restart a plain kill
	case "windows":
		stopDirect(c) // the daemon exits cleanly, so the supervisor ends too
		_ = run("schtasks", "/End", "/TN", c.serviceName())
		return nil
	}
	return run("systemctl", "--user", "stop", c.serviceName()+".service")
}

func systemdUnit(c Config, exe string) string {
	esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%%"), `"`, `\"`) }
	var b strings.Builder
	b.WriteString("# Managed by `consona setup`; removed by `consona setup --undo`.\n[Unit]\nDescription=consona daemon (private packages for npm)\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=\"%s\" daemon run\n", esc(exe))
	for _, kv := range c.serviceEnv() {
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", kv[0], esc(kv[1]))
	}
	fmt.Fprintf(&b, "Restart=always\nRestartSec=1\nStandardOutput=append:%s\nStandardError=append:%s\n\n[Install]\nWantedBy=default.target\n", esc(c.LogPath()), esc(c.LogPath()))
	return b.String()
}

func launchdPlist(c Config, exe string) string {
	x := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	var env strings.Builder
	for _, kv := range c.serviceEnv() {
		fmt.Fprintf(&env, "<key>%s</key><string>%s</string>", x(kv[0]), x(kv[1]))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by consona setup; removed by consona uninstall. -->
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>daemon</string><string>run</string></array>
<key>EnvironmentVariables</key><dict>%s</dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>1</integer>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, x(c.launchdLabel()), x(exe), env.String(), x(c.LogPath()), x(c.LogPath()))
}

// windowsTask is a Task Scheduler definition: start at logon, never time
// out (schtasks' default is 72 hours), keep running on battery, and run the
// supervisor, which restarts the daemon after a crash.
func windowsTask(c Config, exe string) string {
	x := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>consona daemon (private packages for npm); managed by consona setup</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + x(exe) + `</Command><Arguments>daemon supervise</Arguments></Exec></Actions>
</Task>
`
}

// utf16File encodes s as UTF-16LE with a byte order mark, as schtasks
// expects for /XML.
func utf16File(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// Supervise runs `consona daemon run` and starts it again if it crashes;
// it returns when the daemon exits cleanly (consona daemon stop). Used by
// the Windows logon task, which has no restart-on-crash of its own.
func Supervise(c Config) int {
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	return superviseLoop(func() error {
		logf, err := os.OpenFile(c.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer logf.Close()
		cmd := exec.Command(exe, "daemon", "run")
		cmd.Stdout, cmd.Stderr = logf, logf
		return cmd.Run()
	}, time.Sleep)
}

// superviseLoop restarts run until it succeeds, backing off from 1s to 30s
// while it keeps failing quickly.
func superviseLoop(run func() error, sleep func(time.Duration)) int {
	delay := time.Second
	for {
		start := time.Now()
		if run() == nil {
			return 0
		}
		if time.Since(start) > time.Minute {
			delay = time.Second
		}
		sleep(delay)
		if delay *= 2; delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
}
