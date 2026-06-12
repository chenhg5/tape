package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
)

// renderSession prints a session with a metadata header followed by each
// message, color-coded by role. Tool calls show name + truncated input;
// tool results are hidden unless --full.
func renderSession(app *App, s *model.Session, full bool) {
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Println(app.bold(title))
	fmt.Println(app.gray(strings.Repeat("─", 60)))

	kv := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Printf("  %s %s\n", app.gray(padRightDisp(k, 8)), v)
	}
	kv("id", s.ID)
	kv("agent", app.agentColor(s.Agent))
	kv("model", s.Model)
	kv("cwd", s.CWD)
	kv("branch", s.GitBranch)
	kv("when", fmt.Sprintf("%s → %s  (%d msg)", fmtTime(s.StartedAt), fmtTime(s.UpdatedAt), len(s.Messages)))
	if host := s.Meta["host"]; host != "" {
		kv("host", app.yellow(host))
	}
	fmt.Println()

	for _, m := range s.Messages {
		text := m.Text
		if !full {
			text = truncate(text, 2000)
		}
		ts := app.gray(relTime(m.Timestamp))
		switch m.Role {
		case model.RoleUser:
			fmt.Printf("%s %s  %s\n", app.cyan("▌"), app.bold("user"), ts)
			printIndented(text)
			fmt.Println()
		case model.RoleAssistant:
			if text != "" {
				fmt.Printf("%s %s  %s\n", app.green("▌"), app.bold("assistant"), ts)
				printIndented(text)
			}
			for _, tc := range m.ToolCalls {
				fmt.Printf("  %s %s %s\n",
					app.yellow("⚙"),
					app.bold(tc.Name),
					app.gray(truncDisp(collapseWhitespace(tc.Input), 100)))
			}
			fmt.Println()
		case model.RoleTool:
			if full {
				fmt.Printf("%s %s  %s\n", app.gray("▌"), app.gray("tool"), ts)
				printIndented(text)
				fmt.Println()
			}
		case model.RoleSystem:
			if full {
				fmt.Printf("%s %s\n", app.gray("▌"), app.gray("system"))
				printIndented(text)
				fmt.Println()
			}
		}
	}
}

// printIndented writes s with two-space indent on every line.
func printIndented(s string) {
	if s == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Printf("  %s\n", line)
	}
}

func newShowCmd(app *App) *cobra.Command {
	var full bool
	var dir string
	cmd := &cobra.Command{
		Use:     "show [session-id]",
		Aliases: []string{"play"},
		Short:   "Replay an archived session",
		Long: `Pretty-prints a session: metadata header, then every message in
order, color-coded by role. Tool outputs are folded by default — pass
--full to see them in their entirety.

Run with no arguments on a TTY to pick the session from a numbered
menu (scoped to the current directory by default; use --dir "" for
all). Piped or --json runs always demand <session-id>.`,
		Example: `  tape show                          # interactive on a TTY
  tape show @last
  tape show 7dd2afaf                 # any unique id fragment works
  tape show codex/019ea0af --full`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageErrf("show takes at most one session id (got %d)", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var id string
			switch {
			case len(args) == 1:
				id = args[0]
			case app.interactive():
				// Same --dir semantics as restore: unset = cwd, "" = all.
				var scope string
				if cmd.Flag("dir").Changed {
					scope = resolveDirFilter(dir)
				} else if wd, err := os.Getwd(); err == nil {
					scope = wd
				}
				picked, err := pickSession(cmd.Context(), app, scope, "Pick a session to show:")
				if err != nil {
					if errors.Is(err, errPromptAborted) {
						return usageErrf("cancelled")
					}
					return err
				}
				id = picked.ID
			default:
				return usageErrf("missing session id. try:\n" +
					"  tape show @last           (most recent session)\n" +
					"  tape show <id-fragment>   (e.g. 7dd2afaf)\n" +
					"run 'tape ls' to see available sessions")
			}
			canonical, err := app.resolveSessionID(cmd.Context(), id)
			if err != nil {
				return err
			}
			s, err := app.Archive().Get(cmd.Context(), canonical)
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(s)
			}
			renderSession(app, s, full)
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "include tool outputs and untruncated text")
	cmd.Flags().StringVar(&dir, "dir", "", "scope the interactive menu to a project directory (default: cwd; pass \"\" for all)")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
