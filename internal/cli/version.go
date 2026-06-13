package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// versionInfo bundles everything the version + update commands need.
// We compute it lazily once per process so `tape version` and the
// update flow share the same data without re-walking os.Executable
// on each call.
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Install is the inferred install method: "npm" if the binary
	// lives inside a node_modules tree, "go-install" if it's under
	// $GOBIN / $GOPATH/bin, "homebrew" if under /opt/homebrew or
	// linuxbrew prefixes, else "manual" (raw download / make build).
	// Drives `tape update`'s suggestion.
	Install  string `json:"install"`
	Path     string `json:"path,omitempty"`
	HomeDir  string `json:"tape_home"`
}

// collectVersion runs once and is safe to call from anywhere; it
// never returns an error because the worst case is "unknown commit",
// which is still useful info to print.
func collectVersion(app *App) versionInfo {
	info := versionInfo{
		Version: app.Version,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		HomeDir: app.home,
	}
	if exe, err := os.Executable(); err == nil {
		// resolve symlinks so /usr/local/bin/tape → real path; the
		// install detector needs the real location to match against
		// known prefixes.
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			info.Path = real
		} else {
			info.Path = exe
		}
		info.Install = detectInstall(info.Path)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 7 {
					info.Commit = s.Value[:7]
				} else {
					info.Commit = s.Value
				}
			case "vcs.time":
				// Trim to the date — minute precision adds noise
				// without telling the user anything they care about.
				if len(s.Value) >= 10 {
					info.BuildDate = s.Value[:10]
				}
			}
		}
	}
	return info
}

// detectInstall infers how the user got their tape binary. We don't
// trust env vars (GOPATH might be unset, npm hides its prefix) —
// the binary's filesystem location is the most reliable signal.
//
// Order matters: node_modules → npm wins even if the user has
// linked it into $GOBIN; the npm package always knows how to
// upgrade itself.
func detectInstall(path string) string {
	lp := strings.ToLower(path)
	switch {
	case strings.Contains(lp, "node_modules"):
		return "npm"
	case strings.Contains(lp, "/opt/homebrew/"), strings.Contains(lp, "/usr/local/cellar/"), strings.Contains(lp, "/home/linuxbrew/"):
		return "homebrew"
	}
	if gobin := os.Getenv("GOBIN"); gobin != "" && strings.HasPrefix(path, gobin) {
		return "go-install"
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		if strings.HasPrefix(path, filepath.Join(gopath, "bin")) {
			return "go-install"
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, filepath.Join(home, "go", "bin")) {
			return "go-install"
		}
	}
	return "manual"
}

// newVersionCmd renders the long-form version block. `tape --version`
// (the cobra-builtin flag) keeps printing the short one-liner so
// scripts that already parse it don't break.
func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the binary version, build metadata and install method",
		Long: `Reports the running tape binary's version, the git commit it was
built from, when it was built, the Go toolchain that built it, the
target OS/arch, where the binary lives on disk, and how tape thinks
it was installed (npm / go-install / homebrew / manual). The last
field drives 'tape update' — if you ever wonder "why is update
telling me to use npm?", look here.

For agents and scripts use --json; the schema is stable.

Update checks are NOT performed by this command; see 'tape update
--check' for that. Keeping the two separate means 'tape version'
never makes a network call.`,
		Example: `  tape version
  tape version --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := collectVersion(app)
			if app.useJSON() {
				return emitJSON(info)
			}
			app.lead()
			fmt.Printf("  %s %s\n", app.bold("tape"), app.cyan(info.Version))
			if info.Commit != "" {
				if info.BuildDate != "" {
					fmt.Printf("  %s commit %s (%s)\n", app.gray("·"), info.Commit, info.BuildDate)
				} else {
					fmt.Printf("  %s commit %s\n", app.gray("·"), info.Commit)
				}
			}
			fmt.Printf("  %s built with %s\n", app.gray("·"), info.Go)
			fmt.Printf("  %s platform %s/%s\n", app.gray("·"), info.OS, info.Arch)
			fmt.Printf("  %s install %s\n", app.gray("·"), installLabel(info.Install))
			if info.Path != "" {
				fmt.Printf("  %s path %s\n", app.gray("·"), info.Path)
			}
			fmt.Printf("  %s tape home %s\n", app.gray("·"), info.HomeDir)
			return nil
		},
	}
}

// installLabel humanizes the install enum for the people-facing
// output. The machine-readable value stays the enum so scripts can
// branch on it cleanly.
func installLabel(install string) string {
	switch install {
	case "npm":
		return "npm (@tapeai/tape)"
	case "go-install":
		return "go install"
	case "homebrew":
		return "homebrew"
	case "manual":
		return "manual / system binary"
	default:
		return "unknown"
	}
}
