package cli

import (
	"fmt"
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
	cmd := &cobra.Command{
		Use:     "show <session-id>",
		Aliases: []string{"play"},
		Short:   "Replay an archived session",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := app.Archive().Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			s, err := app.Archive().Get(cmd.Context(), id)
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
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
