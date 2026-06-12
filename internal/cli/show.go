package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
)

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
			if app.jsonOut {
				return emitJSON(s)
			}
			fmt.Printf("\x1b[1m%s\x1b[0m\n", s.Title)
			fmt.Printf("id: %s  agent: %s  model: %s\n", s.ID, s.Agent, orDash(s.Model))
			fmt.Printf("cwd: %s  branch: %s\n", orDash(s.CWD), orDash(s.GitBranch))
			fmt.Printf("from %s to %s, %d message(s)\n\n", fmtTime(s.StartedAt), fmtTime(s.UpdatedAt), len(s.Messages))
			for _, m := range s.Messages {
				text := m.Text
				if !full {
					text = truncate(text, 2000)
				}
				switch m.Role {
				case model.RoleUser:
					fmt.Printf("\x1b[36m● user\x1b[0m %s\n%s\n\n", fmtTime(m.Timestamp), text)
				case model.RoleAssistant:
					if text != "" {
						fmt.Printf("\x1b[32m● assistant\x1b[0m %s\n%s\n", fmtTime(m.Timestamp), text)
					}
					for _, tc := range m.ToolCalls {
						fmt.Printf("\x1b[33m  ⚙ %s\x1b[0m %s\n", tc.Name, truncate(tc.Input, 120))
					}
					fmt.Println()
				case model.RoleTool:
					if full {
						fmt.Printf("\x1b[90m● tool\x1b[0m\n%s\n\n", text)
					}
				}
			}
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
