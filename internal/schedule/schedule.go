// Package schedule wraps "ask the OS to run tape sync every N
// minutes" in one verb: Install / Uninstall / Status. The three
// backends — systemd user timers (Linux), launchd LaunchAgents
// (macOS), and Task Scheduler (Windows) — all do the same thing
// from the user's perspective but disagree about every detail.
// Centralizing them here keeps the CLI command thin and lets the
// per-platform plumbing carry the burden of being correct.
//
// The package writes user-scoped jobs only (no root, no system
// services); a CLI tool installing system-wide timers from a
// random `tape sync --install` would be hostile. That also means
// we never need elevation, and uninstalls are idempotent.
//
// Each backend's payload is human-readable on purpose: a systemd
// timer is a plain unit file in ~/.config, a launchd job is a
// plist in ~/Library, and Windows prints the schtasks command for
// the user to run (because there isn't a clean file-scoped
// equivalent that doesn't require XML and an msiexec dance).
package schedule

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Job is what every backend takes. Binary is the tape executable
// to launch; Interval is the period between runs (validated to be
// >= 1 minute; finer than that just generates pointless I/O).
// HomeDir is where the backend should drop user files (mostly so
// tests can redirect away from $HOME).
type Job struct {
	Binary   string        // absolute path to the tape executable
	Args     []string      // typically []string{"sync"}; can be []{"sync","--remote","host"}
	Interval time.Duration // minimum 1m, default 1h
	HomeDir  string        // user home for file placement; defaults to os.UserHomeDir()
}

// Status describes what's currently installed: Installed is the
// authoritative boolean, Path points at the on-disk unit/plist
// (empty on Windows), and Detail is a free-form one-liner for the
// human output.
type Status struct {
	Backend   string `json:"backend"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Interval  string `json:"interval,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Backend is the platform-selected implementation. Callers pick
// one with Detect(); a typed interface (instead of a string switch)
// keeps the cli/sync.go command body honest about what each backend
// does.
type Backend interface {
	Name() string
	Install(j Job) (Status, error)
	Uninstall() (Status, error)
	Current() (Status, error)
}

// Detect returns the right backend for the current OS plus a
// human-readable hint if the platform isn't supported (in which
// case the returned backend is the noop one — Install reports
// "no scheduler for <os>; here's the manual command" instead of
// silently doing nothing).
func Detect() Backend {
	switch runtime.GOOS {
	case "linux":
		return &systemdBackend{}
	case "darwin":
		return &launchdBackend{}
	case "windows":
		return &windowsBackend{}
	default:
		return &manualBackend{os: runtime.GOOS}
	}
}

// jobCommandLine renders Job.Binary + Job.Args as a shell-quoted
// command string. We use it for systemd ExecStart, launchd
// ProgramArguments serialization (with a split), and the Windows
// schtasks command. POSIX single-quote escaping for everything
// except Windows, which gets backslash-quoted at its own backend.
func jobCommandLine(j Job) string {
	parts := append([]string{j.Binary}, j.Args...)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, shellQuote(p))
	}
	return strings.Join(out, " ")
}

// shellQuote is a single-quote-only POSIX quoter — same flavor as
// cli/interactive_ls.go's. Duplicated on purpose: this package has
// no business importing cli, and the helper is six lines.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t'\"\\$`!|&;()<>*?[]{}#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resolveHome returns the directory backends should write user
// files under. Honors Job.HomeDir for tests, falling back to
// os.UserHomeDir(); failure to find one is fatal — every backend
// needs a place to land.
func resolveHome(j Job) (string, error) {
	if j.HomeDir != "" {
		return j.HomeDir, nil
	}
	return os.UserHomeDir()
}

// intervalOrDefault clamps Interval to >= 1 minute and defaults to
// 1 hour when zero. We intentionally don't accept sub-minute
// intervals — sync is I/O-heavy enough that more aggressive
// schedules cost more than the freshness gain.
func intervalOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Hour
	}
	if d < time.Minute {
		return time.Minute
	}
	return d
}

// manualBackend is the fallback for platforms we don't ship a
// real backend for. Install prints the cron-line equivalent and
// errors so the CLI can surface a non-zero exit; nothing else
// touches the filesystem.
type manualBackend struct{ os string }

func (m *manualBackend) Name() string { return "manual:" + m.os }

func (m *manualBackend) Install(j Job) (Status, error) {
	interval := intervalOrDefault(j.Interval)
	mins := int(interval / time.Minute)
	if mins < 1 {
		mins = 1
	}
	cron := fmt.Sprintf("*/%d * * * * %s", mins, jobCommandLine(j))
	return Status{
			Backend: m.Name(),
			Detail:  "no native scheduler for " + m.os + "; add this to your crontab: " + cron,
		},
		fmt.Errorf("no automatic scheduler for %s; see the printed cron line", m.os)
}

func (m *manualBackend) Uninstall() (Status, error) {
	return Status{Backend: m.Name(), Detail: "manual backend has nothing to remove"}, nil
}

func (m *manualBackend) Current() (Status, error) {
	return Status{Backend: m.Name()}, nil
}

// systemdBackend writes a user-scoped service + timer pair under
// ~/.config/systemd/user/. It assumes `systemctl --user` works,
// which is true on every desktop Linux distro using systemd
// (i.e. effectively all of them). The unit names are scoped to
// tape so we don't collide with anything else and so Uninstall is
// straightforward.
type systemdBackend struct{}

const (
	systemdServiceName = "tape-sync.service"
	systemdTimerName   = "tape-sync.timer"
)

func (b *systemdBackend) Name() string { return "systemd" }

func (b *systemdBackend) unitDir(j Job) (string, error) {
	home, err := resolveHome(j)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func (b *systemdBackend) Install(j Job) (Status, error) {
	dir, err := b.unitDir(j)
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Status{Backend: b.Name()}, err
	}
	interval := intervalOrDefault(j.Interval)
	service := fmt.Sprintf(`[Unit]
Description=tape: archive new sessions from all installed agents

[Service]
Type=oneshot
ExecStart=%s
`, jobCommandLine(j))
	timer := fmt.Sprintf(`[Unit]
Description=tape sync every %s

[Timer]
OnBootSec=%s
OnUnitActiveSec=%s
Unit=%s

[Install]
WantedBy=timers.target
`, interval, interval, interval, systemdServiceName)

	servicePath := filepath.Join(dir, systemdServiceName)
	timerPath := filepath.Join(dir, systemdTimerName)
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return Status{Backend: b.Name()}, err
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0o644); err != nil {
		return Status{Backend: b.Name()}, err
	}
	// Enable + start the timer. We swallow errors so the install
	// still succeeds in CI / container environments where the
	// user bus isn't running; the caller surfaces a hint via
	// Status.Detail.
	enableOK := runSilently("systemctl", "--user", "daemon-reload") == nil &&
		runSilently("systemctl", "--user", "enable", "--now", systemdTimerName) == nil
	detail := "wrote " + timerPath
	if enableOK {
		detail += " and enabled the timer"
	} else {
		detail += "; run `systemctl --user daemon-reload && systemctl --user enable --now " + systemdTimerName + "` to start it"
	}
	return Status{
		Backend: b.Name(), Installed: true, Path: timerPath,
		Interval: interval.String(), Detail: detail,
	}, nil
}

func (b *systemdBackend) Uninstall() (Status, error) {
	dir, err := b.unitDir(Job{})
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	// Stop + disable first so we don't leave a running timer
	// orphaned to a missing unit file.
	_ = runSilently("systemctl", "--user", "disable", "--now", systemdTimerName)
	servicePath := filepath.Join(dir, systemdServiceName)
	timerPath := filepath.Join(dir, systemdTimerName)
	for _, p := range []string{servicePath, timerPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return Status{Backend: b.Name()}, err
		}
	}
	_ = runSilently("systemctl", "--user", "daemon-reload")
	return Status{Backend: b.Name(), Installed: false, Detail: "removed " + timerPath}, nil
}

func (b *systemdBackend) Current() (Status, error) {
	dir, err := b.unitDir(Job{})
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	timerPath := filepath.Join(dir, systemdTimerName)
	if _, err := os.Stat(timerPath); err == nil {
		return Status{Backend: b.Name(), Installed: true, Path: timerPath}, nil
	}
	return Status{Backend: b.Name(), Installed: false}, nil
}

// launchdBackend writes a LaunchAgent plist under
// ~/Library/LaunchAgents/. macOS picks it up on the next login,
// or now if we successfully `launchctl bootstrap`.
type launchdBackend struct{}

const launchdLabel = "com.tapeai.sync"

func (b *launchdBackend) Name() string { return "launchd" }

func (b *launchdBackend) plistPath(j Job) (string, error) {
	home, err := resolveHome(j)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func (b *launchdBackend) Install(j Job) (Status, error) {
	path, err := b.plistPath(j)
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{Backend: b.Name()}, err
	}
	interval := intervalOrDefault(j.Interval)
	seconds := int(interval.Seconds())
	if seconds < 60 {
		seconds = 60
	}
	args := append([]string{j.Binary}, j.Args...)
	var argsXML strings.Builder
	for _, a := range args {
		argsXML.WriteString("    <string>")
		argsXML.WriteString(escapeXML(a))
		argsXML.WriteString("</string>\n")
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
`, launchdLabel, argsXML.String(), seconds)

	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return Status{Backend: b.Name()}, err
	}
	// Best-effort: unload (in case we're replacing) then load.
	_ = runSilently("launchctl", "unload", path)
	loadOK := runSilently("launchctl", "load", path) == nil
	detail := "wrote " + path
	if loadOK {
		detail += " and loaded into launchd"
	} else {
		detail += "; run `launchctl load " + path + "` to start it"
	}
	return Status{
		Backend: b.Name(), Installed: true, Path: path,
		Interval: interval.String(), Detail: detail,
	}, nil
}

func (b *launchdBackend) Uninstall() (Status, error) {
	path, err := b.plistPath(Job{})
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	_ = runSilently("launchctl", "unload", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Status{Backend: b.Name()}, err
	}
	return Status{Backend: b.Name(), Installed: false, Detail: "removed " + path}, nil
}

func (b *launchdBackend) Current() (Status, error) {
	path, err := b.plistPath(Job{})
	if err != nil {
		return Status{Backend: b.Name()}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Status{Backend: b.Name(), Installed: true, Path: path}, nil
	}
	return Status{Backend: b.Name(), Installed: false}, nil
}

// escapeXML is the smallest possible plist-safe escaper for the
// five characters that matter in a CDATA-less <string>.
func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;", "'", "&apos;",
	)
	return r.Replace(s)
}

// windowsBackend prints the schtasks command for the user to run.
// Auto-executing schtasks would either need admin (system-wide) or
// would interactively ask for their login password (user-scope),
// which is no better than copy-pasting a one-liner.
type windowsBackend struct{}

func (b *windowsBackend) Name() string { return "schtasks" }

func (b *windowsBackend) Install(j Job) (Status, error) {
	interval := intervalOrDefault(j.Interval)
	mins := int(interval / time.Minute)
	if mins < 1 {
		mins = 1
	}
	// Windows quoting is its own world: we wrap the whole tr
	// command in double quotes and don't try to escape further;
	// users with exotic paths can edit the printed command.
	command := fmt.Sprintf(`schtasks /Create /SC MINUTE /MO %d /TN "TapeSync" /TR "\"%s\" %s" /F`,
		mins, j.Binary, strings.Join(j.Args, " "))
	return Status{
			Backend: b.Name(),
			Detail:  "tape can't install Windows tasks itself; run: " + command,
		},
		fmt.Errorf("Windows install is manual; copy the printed schtasks command")
}

func (b *windowsBackend) Uninstall() (Status, error) {
	return Status{
			Backend: b.Name(),
			Detail:  `to remove: schtasks /Delete /TN "TapeSync" /F`,
		},
		fmt.Errorf("Windows uninstall is manual; copy the printed schtasks command")
}

func (b *windowsBackend) Current() (Status, error) {
	return Status{Backend: b.Name()}, nil
}

// runSilently runs an external command swallowing its output; we
// only care whether it succeeded. exec.LookPath isn't used because
// systemctl/launchctl are guaranteed-present on their platforms
// (or we wouldn't be on this backend in the first place).
func runSilently(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
