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

const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitNoResults = 3
)

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
		Use:           "tape",
		Short:         "Record, search and replay your AI coding sessions",
		Long:          "Tape archives sessions from Claude Code, Codex and Cursor into one place you own.\nNothing gets lost on tape.",
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	defaultDir := os.Getenv("TAPE_DIR")
	if defaultDir == "" {
		home, _ := os.UserHomeDir()
		defaultDir = filepath.Join(home, ".tape")
	}
	root.PersistentFlags().StringVar(&app.dir, "dir", defaultDir, "tape data directory (env: TAPE_DIR)")
	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false, "machine-readable JSON output")

	root.AddCommand(newSyncCmd(app), newLsCmd(app), newSearchCmd(app), newShowCmd(app))

	err := root.Execute()
	app.Close()
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrNoResults):
		fmt.Fprintln(os.Stderr, "tape: no results")
		return ExitNoResults
	case isUsageError(err):
		fmt.Fprintln(os.Stderr, "tape:", err)
		return ExitUsage
	default:
		fmt.Fprintln(os.Stderr, "tape:", err)
		return ExitError
	}
}

func isUsageError(err error) bool {
	var u usageError
	return errors.As(err, &u)
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
