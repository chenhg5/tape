package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// renderSessionList prints sessions in a tight, colored table with
// rune-aware column widths. Columns: id · agent · updated · msgs · title.
// Project is shown as a subtle prefix on the title when it differs from
// the title, to save horizontal space.
func renderSessionList(app *App, sums []model.Summary) {
	const idW, agentW, timeW, msgsW = 22, 11, 9, 6

	header := fmt.Sprintf("  %s  %s  %s  %s  %s",
		padRightDisp("ID", idW),
		padRightDisp("AGENT", agentW),
		padRightDisp("UPDATED", timeW),
		padRightDisp("MSGS", msgsW),
		"TITLE")
	fmt.Println(app.gray(header))

	for _, s := range sums {
		title := s.Title
		if title == "" {
			title = s.Project
		}
		titleCol := truncDisp(title, 60)
		if s.Project != "" && s.Title != "" {
			titleCol = app.gray(truncDisp(s.Project, 28)) + "  " + titleCol
		}
		id := padRightDisp(shortID(s.ID), idW)
		agent := padRightDisp(s.Agent, agentW)
		fmt.Printf("  %s  %s  %s  %s  %s\n",
			app.cyan(shortID(s.ID))+id[len(shortID(s.ID)):],
			app.agentColor(s.Agent)+agent[len(s.Agent):],
			padRightDisp(relTime(s.UpdatedAt), timeW),
			padRightDisp(fmt.Sprintf("%d", s.MsgCount), msgsW),
			titleCol)
	}
	fmt.Printf("\n%s\n", app.gray(fmt.Sprintf("%d session(s). tape show <id> to replay.", len(sums))))
}

func newLsCmd(app *App) *cobra.Command {
	var agent, project, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List archived sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
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
				Agent: agent, Project: project, Since: sinceTime, Limit: limit,
			})
			if err != nil {
				return err
			}
			if app.useJSON() {
				if sums == nil {
					sums = []model.Summary{} // JSON [] not null
				}
				if err := emitJSON(map[string]any{"sessions": sums, "count": len(sums)}); err != nil {
					return err
				}
				if len(sums) == 0 {
					return ErrNoResults
				}
				return nil
			}
			if len(sums) == 0 {
				return ErrNoResults
			}
			renderSessionList(app, sums)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent (claude-code, codex, cursor)")
	cmd.Flags().StringVar(&project, "project", "", "filter by project path ('.' = current dir)")
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
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
