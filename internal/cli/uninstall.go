package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/oplog"
	"github.com/chenhg5/tape/internal/schedule"
)

// newUninstallCmd is the inverse of "I'm done with tape, get it
// off my machine": it walks the side-effects tape left behind
// (scheduler unit, archive + index under $TAPE_HOME, operations
// log) and removes them, then prints the install-method-specific
// command to uninstall the binary itself.
//
// We never uninstall the binary directly. Two reasons:
//   - npm and `go install` are the only ones we'd know how to
//     drive; homebrew + manual installs need the user. Echoing the
//     command is honest about what's happening.
//   - `tape uninstall` deleting `/usr/local/bin/tape` mid-execution
//     would leave the running process unmoored. Bash users would
//     notice; agent automation might not. Better to print.
//
// --force skips the confirmation prompt (useful for CI and
// scripted teardowns); --keep-archive removes only the scheduler
// + oplog and leaves the session archive intact, for users moving
// tape between machines.
func newUninstallCmd(app *App) *cobra.Command {
	var force, keepArchive, dryRun bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove tape's local state and unschedule any periodic sync",
		Long: `Cleans up everything tape installed under your user account:

  1. Unschedules the periodic sync job ('tape sync --uninstall'),
     i.e. removes the systemd user timer / LaunchAgent.
  2. Deletes the archive + index + redaction logs at $TAPE_HOME
     (default ~/.tape). Pass --keep-archive to skip this step.
  3. Removes the operations log (~/.tape/operations.log). Subset of
     step 2 unless --keep-archive is set; harmless to repeat.
  4. Prints the command to remove the tape binary itself, picked
     from the install method 'tape version' reports (npm / go
     install / homebrew / manual). Tape never deletes its own
     binary — bash users notice, agents might not, both deserve
     consent.

By default this prompts for confirmation. --force skips it (for
CI/scripted teardowns); --dry-run shows what would happen and
exits 10.`,
		Example: `  tape uninstall --dry-run      # preview
  tape uninstall                # interactive confirm
  tape uninstall --force        # no prompt
  tape uninstall --keep-archive # remove scheduler/oplog, keep sessions`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			run := startRun(app, "uninstall")
			defer func() { err = run.finish(err) }()

			plan := buildUninstallPlan(app, keepArchive)
			if app.useJSON() {
				if err := emitJSON(map[string]any{
					"plan": plan, "dry_run": dryRun, "force": force,
				}); err != nil {
					return err
				}
				if dryRun {
					return errDryRun
				}
				return runUninstallPlan(app, plan, run)
			}
			renderUninstallPlan(app, plan)
			if dryRun {
				return errDryRun
			}
			if !force {
				if !confirmDestructive(app, "Remove the listed items?") {
					return usageErrf("cancelled")
				}
			}
			return runUninstallPlan(app, plan, run)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&keepArchive, "keep-archive", false, "leave the session archive in place (only remove scheduler + oplog)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without removing anything (exit 10)")
	return cmd
}

// uninstallStep describes one removal action so the dry-run /
// JSON output / actual runner share the same source of truth.
// Kind is enum-ish: "scheduler" / "archive" / "oplog" / "binary".
// Path is filled when relevant (binary's "path" is the printed
// uninstall command, not a filesystem path).
type uninstallStep struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Path   string `json:"path,omitempty"`
	// Suggestion is the literal command we'd ask the user to run
	// for steps we deliberately don't automate (binary removal).
	Suggestion string `json:"suggestion,omitempty"`
}

func buildUninstallPlan(app *App, keepArchive bool) []uninstallStep {
	var steps []uninstallStep
	if cur, err := schedule.Detect().Current(); err == nil && cur.Installed {
		steps = append(steps, uninstallStep{
			Kind: "scheduler", Detail: "unschedule periodic sync (" + cur.Backend + ")",
			Path: cur.Path,
		})
	}
	if !keepArchive {
		if _, err := os.Stat(app.home); err == nil {
			steps = append(steps, uninstallStep{
				Kind: "archive", Detail: "delete archive + index + memory under " + app.home,
				Path: app.home,
			})
		}
	} else {
		// keep-archive mode still drops oplog (it's user-action
		// metadata, not session content).
		if _, err := os.Stat(oplog.Path(app.home)); err == nil {
			steps = append(steps, uninstallStep{
				Kind: "oplog", Detail: "delete operations log",
				Path: oplog.Path(app.home),
			})
		}
	}
	info := collectVersion(app)
	suggestion := uninstallSuggestion(info.Install)
	steps = append(steps, uninstallStep{
		Kind: "binary", Detail: "remove the tape binary itself (not automated)",
		Path: info.Path, Suggestion: suggestion,
	})
	return steps
}

func uninstallSuggestion(install string) string {
	switch install {
	case "npm":
		return "npm uninstall -g @tapeai/tape"
	case "go-install":
		// `go install` doesn't have an uninstall verb; the binary
		// just sits in $GOBIN until you delete it. Tell the user
		// the right `rm` for their layout (we don't try harder
		// because $GOBIN / $GOPATH/bin / $HOME/go/bin all coexist).
		return "rm $(go env GOBIN)/tape   # or $(go env GOPATH)/bin/tape"
	case "homebrew":
		return "brew uninstall tape"
	default:
		return "rm <path-to-tape-binary>   # tape lives at " +
			"<see `tape version` for the exact location>"
	}
}

func renderUninstallPlan(app *App, plan []uninstallStep) {
	app.lead()
	fmt.Printf("  %s will:\n", app.bold("uninstall"))
	for _, s := range plan {
		mark := app.gray("·")
		switch s.Kind {
		case "scheduler", "archive", "oplog":
			mark = app.red("✗")
		case "binary":
			mark = app.yellow("→")
		}
		fmt.Printf("    %s %s\n", mark, s.Detail)
		if s.Path != "" && s.Kind != "binary" {
			fmt.Printf("      %s %s\n", app.gray("path"), s.Path)
		}
		if s.Suggestion != "" {
			fmt.Printf("      %s %s\n", app.gray("run"), app.bold(s.Suggestion))
		}
	}
	fmt.Println()
}

func runUninstallPlan(app *App, plan []uninstallStep, run *recordedRun) error {
	removed := 0
	for _, s := range plan {
		switch s.Kind {
		case "scheduler":
			if _, err := schedule.Detect().Uninstall(); err != nil {
				return fmt.Errorf("unschedule: %w", err)
			}
			removed++
		case "archive":
			if err := os.RemoveAll(app.home); err != nil {
				return fmt.Errorf("remove %s: %w", app.home, err)
			}
			removed++
		case "oplog":
			if err := os.Remove(oplog.Path(app.home)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove oplog: %w", err)
			}
			removed++
		case "binary":
			// Print-only on purpose; see the command Long doc.
		}
	}
	if run != nil {
		run.setCount("removed_steps", removed)
	}
	if !app.useJSON() {
		fmt.Printf("  %s removed %d item(s); run the printed command above to remove the binary\n",
			app.green("✓"), removed)
	}
	return nil
}

// confirmDestructive is a tiny stdin yes/no prompt for the human
// path. We accept y/Y/yes/Yes; everything else (including empty
// Enter) is treated as "no" — irreversible operations should
// require explicit consent. Non-TTY callers should pass --force
// instead; we return false rather than blocking.
func confirmDestructive(app *App, prompt string) bool {
	if !app.interactive() {
		return false
	}
	fmt.Fprintf(os.Stderr, "  %s %s [y/N]: ", app.yellow("?"), prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	resp := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return resp == "y" || resp == "yes"
}

// ensure filepath isn't reported as unused even if rare paths
// strip every reference.
var _ = filepath.Join
