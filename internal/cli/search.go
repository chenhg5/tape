package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
)

func newSearchCmd(app *App) *cobra.Command {
	var agent, project, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across all archived sessions",
		Long:  "Search every archived session. Latin words match by prefix; Chinese/Japanese/Korean text is matched by character bigrams, so CJK queries just work.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			ix, err := app.Index()
			if err != nil {
				return err
			}
			hits, err := ix.Search(cmd.Context(), ports.Query{
				Text:    strings.Join(args, " "),
				Agent:   agent,
				Project: project,
				Since:   t,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			if app.useJSON() {
				if err := emitJSON(map[string]any{"hits": hits, "count": len(hits)}); err != nil {
					return err
				}
				if len(hits) == 0 {
					return ErrNoResults
				}
				return nil
			}
			if len(hits) == 0 {
				return ErrNoResults
			}
			for _, h := range hits {
				title := h.Title
				if title == "" {
					title = h.Project
				}
				fmt.Printf("%s  %s  [%s] %s\n", app.bold(shortID(h.SessionID)), fmtTime(h.Timestamp), h.Role, truncate(title, 60))
				fmt.Printf("  %s\n\n", h.Snippet)
			}
			fmt.Printf("%d hit(s). Use `tape show <id>` to replay a session.\n", len(hits))
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent")
	cmd.Flags().StringVar(&project, "project", "", "filter by project path")
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max hits")
	return cmd
}
