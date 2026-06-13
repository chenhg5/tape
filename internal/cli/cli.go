// Package cli wires tape's commands. Every command speaks two dialects:
// human-readable output by default, and a single stable JSON object with
// --json (schema_version included) for agents and scripts.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/archive/local"
	"github.com/chenhg5/tape/internal/config"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/index/sqlitefts"
)

const schemaVersion = 1

// Prefix matching lets users type any unambiguous prefix of a subcommand:
// `tape sy` → sync, `tape sho` → show, `tape se` → search. Ambiguous
// prefixes (e.g. `s` matches sync/search/show/schema) fall through to
// cobra's "did you mean" output.
func init() { cobra.EnablePrefixMatching = true }

// ErrNoResults maps to exit code 3 so agents can branch on "found nothing"
// without parsing output.
var ErrNoResults = errors.New("no results")

// errDryRun maps to exit code 10: the dry run succeeded and it is safe to
// run for real (agent-cli-guide principle 5).
var errDryRun = errors.New("dry run ok")

const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitNoResults = 3
	ExitDryRunOK  = 10
)

const exitCodeHelp = `Exit codes:
  0  success
  1  error
  2  usage error
  3  no results / not found
  10 dry run succeeded (safe to run without --dry-run)`

// usageTemplate mirrors cobra's default but moves Examples after Flags so
// help reads top-down as: synopsis → structure → worked examples.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// App holds process-wide state for a single CLI invocation.
//
// home is where tape stores its own state (archive + index). Users almost
// never need to think about it; the default ~/.tape is right for everyone
// who does not multi-tenant. We deliberately do NOT expose it as a flag
// because it has nothing to do with the project the user is working on —
// the flag `--dir` in ls/search refers to the project directory instead,
// matching the user's mental model ("I want my sessions for this repo").
// To relocate the archive, set TAPE_HOME=/somewhere/else.
type App struct {
	Sources []ports.Source
	// SourceFactory builds the source set for any home directory; used to
	// parse SSH-mirrored remote homes with the same parsers.
	SourceFactory func(home string) []ports.Source
	Version       string

	home     string
	jsonOut  bool
	debug    bool
	archive  *local.Archive
	indexImp *sqlitefts.Index
	defaults *config.Defaults
}

// Defaults returns the user's persisted preferences from
// ~/.tape/config.json. We cache after the first read so commands
// can call it freely without thinking about I/O. A bad file is
// reported through debugf (so --debug surfaces it) but never
// fails the invocation; users editing config in vim shouldn't be
// able to brick `tape ls`.
func (a *App) Defaults() config.Defaults {
	if a.defaults != nil {
		return *a.defaults
	}
	c, err := config.Load(a.home)
	if err != nil {
		a.debugf("config load failed (ignored): %v", err)
		c = &config.Config{}
	}
	a.defaults = &c.Defaults
	return c.Defaults
}

// debugf prints to stderr only when --debug / -v is on. The prefix
// keeps debug noise visually distinct from real output (which goes
// to stdout) and from regular stderr warnings. Format mirrors fmt
// so callers don't have to think about a custom logger interface.
func (a *App) debugf(format string, args ...any) {
	if a == nil || !a.debug {
		return
	}
	prefix := "[debug] "
	if a.useColor() {
		prefix = a.gray("[debug] ")
	}
	fmt.Fprintf(os.Stderr, prefix+format+"\n", args...)
}

func (a *App) Archive() *local.Archive {
	if a.archive == nil {
		a.archive = local.New(filepath.Join(a.home, "archive"))
	}
	return a.archive
}

func (a *App) Index() (ports.Index, error) {
	if a.indexImp == nil {
		ix, err := sqlitefts.Open(filepath.Join(a.home, "index", "tape.db"))
		if err != nil {
			return nil, err
		}
		a.indexImp = ix
	}
	return a.indexImp, nil
}

// resolveSessionID expands the user-facing id reference into the canonical
// "<agent>/<full-uuid>" form the archive layer expects. It accepts:
//
//   - "@last" — the most recently updated session
//   - "<agent>/<uuid>" — already canonical, returned as-is
//   - any unique id fragment — resolved against the archive
//
// Centralized here so every command (show, restore, ...) accepts the
// same shorthand without re-implementing the lookup.
func (a *App) resolveSessionID(ctx context.Context, ref string) (string, error) {
	if ref == "@last" {
		sums, err := a.Archive().List(ctx, ports.Filter{Limit: 1})
		if err != nil {
			return "", err
		}
		if len(sums) == 0 {
			return "", ErrNoResults
		}
		return sums[0].ID, nil
	}
	return a.Archive().Resolve(ctx, ref)
}

// resolveHome picks where tape keeps its archive. Priority:
//  1. $TAPE_HOME       (preferred name; "tape's home directory")
//  2. $TAPE_DIR        (kept for backward compatibility with older docs/tests)
//  3. ~/.tape          (the only path 99% of users ever see)
func resolveHome() string {
	if v := os.Getenv("TAPE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("TAPE_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tape")
}

func (a *App) Close() {
	if a.indexImp != nil {
		a.indexImp.Close()
	}
}

func Execute(app *App) int {
	root := &cobra.Command{
		Use:   "tape",
		Short: "Record, search and replay your AI coding sessions",
		Long: `Tape archives sessions from Claude Code, Codex and Cursor into one place
you own. Output is human-readable on a TTY and JSON when piped (or with --json).`,
		Example: `  tape sync                          archive new sessions from all agents
  tape search "为什么不用 oauth2"      full-text search, CJK supported
  tape restore codex/019ea0af --to claude-code`,
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Custom usage template: Examples are pushed below Commands+Flags so
	// the structural sections come first and the worked examples sit
	// right above the prompt — easier to skim, easier to copy/paste from.
	root.SetUsageTemplate(usageTemplate)

	// Show the splash banner on top-level --help / -h, then delegate to
	// cobra's default renderer, and append Exit codes after everything
	// else. Subcommand help is untouched (no banner, no exit-code block).
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == root && !app.useJSON() {
			fmt.Fprintln(os.Stdout, banner(app, app.Version))
		}
		defaultHelp(cmd, args)
		if cmd == root {
			fmt.Fprintln(os.Stdout)
			fmt.Fprintln(os.Stdout, exitCodeHelp)
		}
	})
	// flag parsing problems are usage errors (exit 2), not runtime errors
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})
	app.home = resolveHome()
	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false, "machine-readable JSON output")
	root.PersistentFlags().BoolVarP(&app.debug, "debug", "v", false, "verbose diagnostic logging on stderr (source detection, SQL filters, scheduler payloads)")

	root.AddCommand(
		newSyncCmd(app), newLsCmd(app), newSearchCmd(app), newShowCmd(app),
		newOverviewCmd(app), newExportCmd(app), newRestoreCmd(app),
		newIndexCmd(app), newSchemaCmd(app, root),
		newVersionCmd(app), newUpdateCmd(app),
		newCompletionCmd(app), newHistoryCmd(app), newConfigCmd(app),
		newUninstallCmd(app),
	)

	err := root.Execute()
	app.Close()
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, errDryRun):
		return ExitDryRunOK
	case errors.Is(err, ErrNoResults):
		app.reportError(cliError{Type: "no_results", Message: "no results"})
		return ExitNoResults
	case isUsageError(err) || isCobraUsage(err):
		app.reportError(cliError{Type: "usage", Message: err.Error()})
		return ExitUsage
	default:
		app.reportError(err)
		return ExitError
	}
}

func isUsageError(err error) bool {
	var u usageError
	return errors.As(err, &u)
}

// isCobraUsage catches the usage errors cobra produces itself (unknown
// command, wrong arg count) so they also map to exit 2.
func isCobraUsage(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "accepts ", "requires at least", "unknown shorthand"} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

type usageError struct{ error }

func usageErrf(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// emitJSON prints the single JSON object contract of robot mode.
func emitJSON(payload any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		SchemaVersion int `json:"schema_version"`
		Data          any `json:"data"`
	}{schemaVersion, payload})
}

// parseSince accepts durations like 24h / 7d / 30d or YYYY-MM-DD dates.
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s[:n-1], "%d", &days); err == nil {
			return time.Now().AddDate(0, 0, -days), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, usageErrf("invalid --since %q (try 24h, 7d or 2026-01-31)", s)
	}
	return time.Now().Add(-d), nil
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
