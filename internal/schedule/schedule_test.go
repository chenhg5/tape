package schedule

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDetectMatchesRuntime guards against accidentally returning
// the manual backend on a supported platform — easy regression if
// someone adds a new switch arm later.
func TestDetectMatchesRuntime(t *testing.T) {
	b := Detect()
	switch runtime.GOOS {
	case "linux":
		if b.Name() != "systemd" {
			t.Errorf("linux backend = %q, want systemd", b.Name())
		}
	case "darwin":
		if b.Name() != "launchd" {
			t.Errorf("darwin backend = %q, want launchd", b.Name())
		}
	case "windows":
		if b.Name() != "schtasks" {
			t.Errorf("windows backend = %q, want schtasks", b.Name())
		}
	default:
		if !strings.HasPrefix(b.Name(), "manual:") {
			t.Errorf("unknown platform backend = %q, want manual:*", b.Name())
		}
	}
}

// TestIntervalOrDefault covers the three branches: zero → 1h,
// sub-minute → 1m, sensible → as-is.
func TestIntervalOrDefault(t *testing.T) {
	if got := intervalOrDefault(0); got != time.Hour {
		t.Errorf("zero: %v, want 1h", got)
	}
	if got := intervalOrDefault(5 * time.Second); got != time.Minute {
		t.Errorf("5s: %v, want 1m", got)
	}
	if got := intervalOrDefault(45 * time.Minute); got != 45*time.Minute {
		t.Errorf("45m: %v, want 45m", got)
	}
}

// TestShellQuoteCoversPosixCorners pins the shape of the strings
// we hand systemd/launchd. Bare safe strings pass through; anything
// with spaces or shell-meaningful chars wraps in single quotes and
// escapes existing single quotes via the classic '\'' trick.
func TestShellQuoteCoversPosixCorners(t *testing.T) {
	cases := map[string]string{
		"":                     "''",
		"tape":                 "tape",
		"/usr/local/bin/tape":  "/usr/local/bin/tape",
		"with space":           "'with space'",
		`already'quoted`:       `'already'\''quoted'`,
		`shell$danger`:         `'shell$danger'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSystemdRoundTrip writes units to a temp HOME, asserts the
// payload is sane, and removes them. We don't try to talk to
// systemctl in tests — that needs a user bus which CI containers
// don't have — but the file-shape check catches every regression
// short of "systemctl actually loads it".
func TestSystemdRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd only meaningful on linux")
	}
	home := t.TempDir()
	b := &systemdBackend{}
	job := Job{
		Binary: "/usr/local/bin/tape", Args: []string{"sync"},
		Interval: 30 * time.Minute, HomeDir: home,
	}

	s, err := b.Install(job)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !s.Installed || !strings.HasSuffix(s.Path, "tape-sync.timer") {
		t.Errorf("install status: %+v", s)
	}
	if s.Interval != "30m0s" {
		t.Errorf("interval echo = %q", s.Interval)
	}

	timer, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OnUnitActiveSec=30m0s", "Unit=tape-sync.service"} {
		if !strings.Contains(string(timer), want) {
			t.Errorf("timer missing %q:\n%s", want, timer)
		}
	}
	servicePath := filepath.Join(filepath.Dir(s.Path), "tape-sync.service")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), "ExecStart=/usr/local/bin/tape sync") {
		t.Errorf("service missing ExecStart line:\n%s", service)
	}

	// Replace via re-install: makes sure we overwrite cleanly.
	job.Interval = time.Hour
	s2, err := b.Install(job)
	if err != nil || !s2.Installed {
		t.Fatalf("re-install: %+v err=%v", s2, err)
	}
	timer2, _ := os.ReadFile(s2.Path)
	if !strings.Contains(string(timer2), "OnUnitActiveSec=1h0m0s") {
		t.Errorf("re-install didn't refresh interval:\n%s", timer2)
	}

	// Current must reflect the on-disk file existence regardless of
	// whether systemctl can load it (the file is the source of truth).
	cur, err := (&systemdBackend{}).currentForTest(home)
	if err != nil || !cur.Installed {
		t.Errorf("status: %+v err=%v", cur, err)
	}
}

// currentForTest is a thin wrapper letting tests inject HOME
// without touching the real one. The package-public Current()
// uses os.UserHomeDir(); the test variant uses Job{}.HomeDir.
func (b *systemdBackend) currentForTest(home string) (Status, error) {
	path := filepath.Join(home, ".config", "systemd", "user", systemdTimerName)
	if _, err := os.Stat(path); err == nil {
		return Status{Backend: b.Name(), Installed: true, Path: path}, nil
	}
	return Status{Backend: b.Name(), Installed: false}, nil
}

// TestLaunchdPlistShape: macOS-only. The launchd loader doesn't run
// in tests so we just assert the plist has the bits launchd needs
// (Label, ProgramArguments, StartInterval) and that quoting +
// escaping survives funny characters in args.
func TestLaunchdPlistShape(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only meaningful on darwin")
	}
	home := t.TempDir()
	b := &launchdBackend{}
	job := Job{
		Binary: "/opt/tape", Args: []string{"sync", "--remote", "user@host<&>"},
		Interval: 30 * time.Minute, HomeDir: home,
	}
	s, err := b.Install(job)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>Label</key>",
		"<string>com.tapeai.sync</string>",
		"<integer>1800</integer>",   // 30 minutes
		"<string>/opt/tape</string>",
		"<string>--remote</string>",
		"&lt;&amp;&gt;",             // XML-escaped
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("plist missing %q:\n%s", want, data)
		}
	}

	// Uninstall removes the file.
	if _, err := b.uninstallForTest(home); err != nil {
		t.Errorf("uninstall: %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Errorf("plist not removed: %v", err)
	}
}

func (b *launchdBackend) uninstallForTest(home string) (Status, error) {
	path := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	_ = runSilently("launchctl", "unload", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Status{Backend: b.Name()}, err
	}
	return Status{Backend: b.Name(), Installed: false}, nil
}

// TestWindowsBackendPrintsCommand: we never auto-exec schtasks, so
// the test is just that the printed command contains the binary
// path + the interval-derived MO.
func TestWindowsBackendPrintsCommand(t *testing.T) {
	b := &windowsBackend{}
	s, err := b.Install(Job{Binary: `C:\tape.exe`, Args: []string{"sync"}, Interval: 45 * time.Minute})
	if err == nil {
		t.Fatalf("install must report an error so the CLI exit code is non-zero, got nil")
	}
	if !strings.Contains(s.Detail, "schtasks /Create") || !strings.Contains(s.Detail, "/MO 45") {
		t.Errorf("detail missing schtasks line: %q", s.Detail)
	}
}

// TestManualBackendCronLine: unsupported platform falls back to
// printing a crontab line; verify the user can copy it as-is.
func TestManualBackendCronLine(t *testing.T) {
	b := &manualBackend{os: "freebsd"}
	s, err := b.Install(Job{Binary: "/usr/local/bin/tape", Args: []string{"sync"}, Interval: 30 * time.Minute})
	if err == nil {
		t.Fatal("install on manual backend must report an error")
	}
	if !strings.Contains(s.Detail, "*/30 * * * * /usr/local/bin/tape sync") {
		t.Errorf("cron line wrong: %q", s.Detail)
	}
}
