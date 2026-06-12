package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/service"
)

func newSyncCmd(app *App) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Archive new or changed sessions from all agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			ix, err := app.Index()
			if err != nil {
				return err
			}
			sync := &service.Sync{Sources: app.Sources, Archive: app.Archive(), Index: ix}
			report, err := sync.Run(cmd.Context(), t)
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(report)
			}
			for _, s := range report.Sources {
				if !s.Found {
					fmt.Printf("%-12s not found\n", s.Agent)
					continue
				}
				fmt.Printf("%-12s scanned %-4d archived %-4d skipped %-4d", s.Agent, s.Scanned, s.Archived, s.Skipped)
				if len(s.Errors) > 0 {
					fmt.Printf(" errors %d", len(s.Errors))
				}
				fmt.Println()
				for _, e := range s.Errors {
					fmt.Printf("  ! %s\n", e)
				}
			}
			fmt.Printf("synced: %d session(s) archived\n", report.Archived())
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only scan sessions updated since (24h, 7d, 2026-01-31)")
	return cmd
}
