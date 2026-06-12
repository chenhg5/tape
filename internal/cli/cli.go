// Package cli wires tape's commands. Every command speaks two dialects:
// human-readable output by default, and a single stable JSON object with
// --json (schema_version included) for agents and scripts.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/archive/local"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/index/sqlitefts"
)

const schemaVersion = 1

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

type App struct {
	Sources []ports.Source
	Version string

	dir      string
	jsonOut  bool
	archive  *local.Archive
	indexImp *sqlitefts.Index
}

func (a *App) Archive() *local.Archive {
	if a.archive == nil {
		a.archive = local.New(filepath.Join(a.dir, "archive"))
	}
	return a.archive
}

func (a *App) Index() (ports.Index, error) {
	if a.indexImp == nil {
		ix, err := sqlitefts.Open(filepath.Join(a.dir, "index", "tape.db"))
		if err != nil {
			return nil, err
		}
		a.indexImp = ix
	}
	return a.indexImp, nil
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
		Long: `Tape archives sessions from Claude Code, Codex and Cursor into one place you own.
Nothing gets lost on tape.

Output is human-readable on a TTY and JSON when piped (or with --json).

` + exitCodeHelp,
		Example: `  tape sync                          archive new sessions from all agents
  tape search "为什么不用 oauth2"      full-text search, CJK supported
  tape restore codex/019ea0af --to claude-code`,
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// flag parsing problems are usage errors (exit 2), not runtime errors
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})
	defaultDir := os.Getenv("TAPE_DIR")
	if defaultDir == "" {
		home, _ := os.UserHomeDir()
		defaultDir = filepath.Join(home, ".tape")
	}
	root.PersistentFlags().StringVar(&app.dir, "dir", defaultDir, "tape data directory (env: TAPE_DIR)")
	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false, "machine-readable JSON output")

	root.AddCommand(
		newSyncCmd(app), newLsCmd(app), newSearchCmd(app), newShowCmd(app),
		newBackupCmd(app), newRestoreCmd(app), newIndexCmd(app), newSchemaCmd(app, root),
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
