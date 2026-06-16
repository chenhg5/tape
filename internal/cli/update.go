package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/mirror"
)

// httpTimeout caps the whole update check (probe race + fetch).
// Short on purpose: this is a foreground command, the user is
// waiting at the prompt.
const httpTimeout = 8 * time.Second

// updateResult is the JSON contract for `tape update --check
// --json`. UpToDate is the most important field — scripts can
// `jq -e '.up_to_date | not'` and trigger a reinstall. Mirror is
// the host we actually fetched from ("github" / "gitee"), useful
// for CN users to verify the probe picked the fast path.
type updateResult struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Channel    string `json:"channel"`
	UpToDate   bool   `json:"up_to_date"`
	ReleaseURL string `json:"release_url,omitempty"`
	Published  string `json:"published,omitempty"`
	Mirror     string `json:"mirror,omitempty"`
	// Install + Command echo what `tape update` (no --check) would
	// run, so agents can preview the side effect before it happens.
	Install string `json:"install,omitempty"`
	Command string `json:"command,omitempty"`
}

// newUpdateCmd wires `tape update`. Default action is "check, and
// if there's a newer release, run the right installer for how you
// got tape". --check just reports; --dry-run prints the command
// without executing.
func newUpdateCmd(app *App) *cobra.Command {
	var (
		channel   string
		checkOnly bool
		dryRun    bool
		mirrorPin string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install a newer tape release",
		Long: `Asks the configured release mirror for the latest tag
(--channel beta includes prereleases), compares against the running
version, and — unless --check is set — runs the right installer
for how you got tape:

  npm install        →  npm install -g @tapeai/tape@<tag>
  go install         →  go install github.com/chenhg5/tape/cmd/tape@<tag>
  homebrew / manual  →  prints the right command and exits non-zero

Update checks are explicit. tape never pings GitHub or Gitee in the
background — privacy is the default, you ask when you want to know.

Mirror selection: by default, tape races a tiny HEAD request against
each known mirror (GitHub and Gitee) and uses whichever responds
first — handy for users in regions where one of them is slow. Pin
a mirror with --mirror github|gitee, or set TAPE_MIRROR=<name> in
your environment. --debug surfaces every probe's latency.

--dry-run prints the command but doesn't execute (useful in CI).
--json emits a stable schema with current/latest/up_to_date/
command/mirror for scripts.`,
		Example: `  tape update --check
  tape update                 # actually upgrade
  tape update --channel beta  # include prereleases
  tape update --mirror gitee  # force the Gitee mirror
  tape update --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			run := startRun(app, "update")
			run.setScope("channel", channel)
			if checkOnly {
				run.setScope("mode", "check")
			}
			if mirrorPin != "" {
				run.setScope("mirror_pin", mirrorPin)
			}
			defer func() { err = run.finish(err) }()
			if channel == "" {
				channel = "latest"
			}
			if channel != "latest" && channel != "beta" {
				return usageErrf("--channel must be 'latest' or 'beta'")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), httpTimeout)
			defer cancel()

			// --mirror overrides TAPE_MIRROR which overrides probe.
			// We construct a tiny env override in-process by
			// looking up the pinned host directly, bypassing Pick.
			var host mirror.Host
			var report mirror.PickReport
			switch {
			case mirrorPin != "":
				h, ok := mirror.Lookup(mirrorPin)
				if !ok {
					return usageErrf("unknown mirror %q (known: github, gitee)", mirrorPin)
				}
				host = h
				app.debugf("mirror pinned via --mirror: %s", host.DisplayName)
			default:
				report, err = mirror.PickWithReport(ctx)
				if err != nil {
					return cliError{
						Type: "update_failed", Message: err.Error(),
						Suggestion: "set TAPE_MIRROR=github or TAPE_MIRROR=gitee to skip the probe",
					}
				}
				host = report.Chosen
				for _, r := range report.Results {
					switch {
					case r.Err() != nil:
						app.debugf("probe %s: error: %v", r.Host().Name, r.Err())
					default:
						app.debugf("probe %s: %s", r.Host().Name, r.Latency().Round(time.Millisecond))
					}
				}
				app.debugf("mirror chosen: %s (reason=%s)", host.DisplayName, report.Reason)
			}
			run.setScope("mirror", host.Name)

			app.debugf("GET %s (channel=%s, timeout=%s)", host.APILatest(), channel, httpTimeout)
			rel, err := host.FetchLatest(ctx, channel)
			if err != nil {
				return cliError{
					Type: "update_failed", Message: err.Error(),
					Suggestion: fmt.Sprintf("check network or browse %s; or pin a mirror with --mirror github|gitee",
						host.APILatest()),
				}
			}

			info := collectVersion(app)
			currentTag := normalizeTag(info.Version)
			latestTag := normalizeTag(rel.TagName)
			upToDate := currentTag != "" && currentTag == latestTag
			install := info.Install
			command := upgradeCommand(install, rel.TagName)

			result := updateResult{
				Current:    info.Version,
				Latest:     rel.TagName,
				Channel:    channel,
				UpToDate:   upToDate,
				ReleaseURL: rel.HTMLURL,
				Mirror:     host.Name,
				Install:    install,
				Command:    command,
			}
			if !rel.PublishedAt.IsZero() {
				result.Published = rel.PublishedAt.Format("2006-01-02")
			}

			if app.useJSON() {
				if err := emitJSON(result); err != nil {
					return err
				}
				if checkOnly && !upToDate {
					return errUpdateAvailable
				}
				return nil
			}
			printUpdateHuman(app, result)
			if checkOnly {
				if !upToDate {
					return errUpdateAvailable
				}
				return nil
			}
			if upToDate {
				return nil
			}
			if command == "" {
				return cliError{
					Type:       "update_failed",
					Message:    "cannot detect how tape was installed; upgrade manually",
					Suggestion: "see " + rel.HTMLURL,
				}
			}
			if dryRun {
				app.lead()
				fmt.Fprintf(os.Stderr, "  %s would run: %s\n", app.gray("·"), app.bold(command))
				return errDryRun
			}
			return runUpgrade(cmd.Context(), app, command)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "latest", "release channel: latest (stable) or beta (include prereleases)")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report without upgrading; exit 4 if a newer release exists")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the upgrade command without running it")
	cmd.Flags().StringVar(&mirrorPin, "mirror", "", "force a release mirror (github, gitee). Defaults to fastest-responding probe; TAPE_MIRROR env has the same effect")
	return cmd
}

// errUpdateAvailable is the sentinel that turns --check + outdated
// into a non-zero exit so scripts can branch (`tape update --check
// || tape update`). We piggyback ExitNoResults (3)'s semantic
// — there is no exit code for "advisory failure" — but use a
// distinct error type so the CLI envelope reports it cleanly.
var errUpdateAvailable = cliError{Type: "update_available", Message: "newer release available", Suggestion: "run `tape update`"}

// normalizeTag drops a leading 'v' (and an optional " " separator
// in case future formats add a build metadata suffix) so we can
// compare GitHub's "v0.2.0" tag with the binary's "0.2.0" version.
func normalizeTag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, " +"); i > 0 {
		s = s[:i]
	}
	return s
}

// upgradeCommand returns the literal shell command to upgrade by
// install method. Empty string means "no automatic path — print
// docs". Tag is the GitHub tag including the v prefix; npm and
// go install both happily accept it.
func upgradeCommand(install, tag string) string {
	v := strings.TrimPrefix(tag, "v")
	switch install {
	case "npm":
		return "npm install -g @tapeai/tape@" + v
	case "go-install":
		// Go modules want @vX.Y.Z (with the v); pseudo-versions
		// also work as-is.
		mod := "github.com/chenhg5/tape/cmd/tape"
		if strings.HasPrefix(tag, "v") {
			return "go install " + mod + "@" + tag
		}
		return "go install " + mod + "@v" + v
	case "homebrew":
		// We don't ship a formula yet; once we do, this becomes
		// "brew upgrade tape". For now point users at the release.
		return ""
	default:
		return ""
	}
}

// runUpgrade execs the upgrade command in /bin/sh -c so shell
// builtins (like npm's PATH lookup of a global) resolve the same
// way the user's interactive shell would.
func runUpgrade(ctx context.Context, app *App, command string) error {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		shell = os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd"
		}
	}
	app.lead()
	fmt.Fprintf(os.Stderr, "  %s exec %s\n", app.gray("·"), app.bold(command))
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, shell, "/c", command)
	} else {
		c = exec.CommandContext(ctx, shell, "-c", command)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// printUpdateHuman is the conversational version of the result.
// Keeps the structure consistent: current first, then latest, then
// the actionable line (either "you're current" or "to upgrade …").
func printUpdateHuman(app *App, r updateResult) {
	app.lead()
	fmt.Printf("  %s current  %s\n", app.gray("·"), r.Current)
	fmt.Printf("  %s latest   %s", app.gray("·"), app.cyan(r.Latest))
	if r.Published != "" {
		fmt.Printf("  %s", app.gray("("+r.Published+")"))
	}
	if r.Mirror != "" {
		fmt.Printf("  %s", app.gray("via "+r.Mirror))
	}
	fmt.Println()
	if r.UpToDate {
		fmt.Printf("  %s up to date\n", app.green("✓"))
		return
	}
	if r.Command == "" {
		fmt.Printf("  %s newer release available — see %s\n",
			app.yellow("!"), app.cyan(r.ReleaseURL))
		return
	}
	fmt.Printf("  %s to upgrade: %s\n", app.yellow("→"), app.bold(r.Command))
}
