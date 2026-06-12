package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
)

func newLsCmd(app *App) *cobra.Command {
	var agent, project string
	var limit int
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List archived sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "." {
				if wd, err := os.Getwd(); err == nil {
					project = wd
				}
			} else if project != "" && project != "." {
				if abs, err := filepath.Abs(project); err == nil {
					if _, err := os.Stat(abs); err == nil {
						project = abs
					}
				}
			}
			sums, err := app.Archive().List(cmd.Context(), ports.Filter{
				Agent: agent, Project: project, Limit: limit,
			})
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(map[string]any{"sessions": sums, "count": len(sums)})
			}
			if len(sums) == 0 {
				return ErrNoResults
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAGENT\tUPDATED\tMSGS\tPROJECT\tTITLE")
			for _, s := range sums {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
					shortID(s.ID), s.Agent, fmtTime(s.UpdatedAt), s.MsgCount, truncate(s.Project, 32), truncate(s.Title, 48))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent (claude-code, codex, cursor)")
	cmd.Flags().StringVar(&project, "project", "", "filter by project path ('.' = current dir)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max sessions to list")
	return cmd
}

// shortID keeps "<agent>/<first-8-of-uuid>" for display; full ids and any
// unique fragment are accepted everywhere ids are read.
func shortID(id string) string {
	if len(id) > 8 {
		for i, r := range id {
			if r == '/' && len(id) > i+9 {
				return id[:i+9]
			}
		}
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
