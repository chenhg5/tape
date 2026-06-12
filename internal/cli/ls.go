package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// resolveDirFilter normalises the --dir filter into the absolute path the
// archive matches against. "." (and the empty string when caller wishes)
// expand to the working directory; a relative path becomes absolute so the
// archive's prefix match works regardless of where the user invokes tape.
// Non-existent paths are still passed through verbatim: users may filter
// for an old project that no longer lives on disk.
func resolveDirFilter(raw string) string {
	if raw == "" {
		return ""
	}
	if raw == "." {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return raw
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return raw
}

// renderSessionList prints sessions in a tight, colored table with
// rune-aware column widths. Columns: id · agent · updated · msgs · title.
// Project is shown as a subtle prefix on the title when it differs from
// the title, to save horizontal space.
func renderSessionList(app *App, sums []model.Summary, page, pageSize, total int) {
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
	footer := fmt.Sprintf("%d of %d session(s)", len(sums), total)
	if total > pageSize {
		pages := (total + pageSize - 1) / pageSize
		footer += fmt.Sprintf("  ·  page %d/%d", page, pages)
		if page*pageSize < total {
			footer += fmt.Sprintf("  ·  --page %d for more", page+1)
		}
	}
	fmt.Printf("\n%s\n", app.gray(footer+".  tape show <id> to replay."))
}

func newLsCmd(app *App) *cobra.Command {
	var agent, dir, since string
	var limit, page int
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List archived sessions",
		Long: `List archived sessions, newest first.

--dir <path> restricts the list to sessions whose working directory was
<path> (or one of its descendants). Use "." for the current directory —
this is the most common filter, e.g. "tape ls --dir ." inside a repo.

Pagination: --limit N --page P returns page P (1-based) of N items each. JSON
output always includes total/page/has_more so agents can iterate without
parsing prose.`,
		Example: `  tape ls --dir .                    # sessions for the current repo
  tape ls --agent codex
  tape ls --limit 20 --page 3        # third page of twenty`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if page < 1 {
				return usageErrf("--page must be >= 1")
			}
			if limit < 1 {
				return usageErrf("--limit must be >= 1")
			}
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
			dir = resolveDirFilter(dir)
			filter := ports.Filter{
				Agent: agent, Project: dir, Since: sinceTime,
				Limit: limit, Offset: (page - 1) * limit,
			}
			sums, err := app.Archive().List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			total, err := app.Archive().Count(cmd.Context(), filter)
			if err != nil {
				return err
			}
			hasMore := page*limit < total
			if app.useJSON() {
				if sums == nil {
					sums = []model.Summary{}
				}
				if err := emitJSON(map[string]any{
					"sessions": sums, "count": len(sums),
					"total": total, "page": page, "page_size": limit, "has_more": hasMore,
				}); err != nil {
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
			renderSessionList(app, sums, page, limit, total)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent (claude-code, codex, cursor, opencode, gemini, antigravity, qwen, iflow, qoder, aider)")
	cmd.Flags().StringVar(&dir, "dir", "", "filter by project directory ('.' = current dir)")
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().IntVar(&limit, "limit", 20, "page size")
	cmd.Flags().IntVar(&page, "page", 1, "page number (1-based)")
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
