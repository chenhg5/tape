package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/service"
	"github.com/chenhg5/tape/internal/remote"
)

func newSyncCmd(app *App) *cobra.Command {
	var since string
	var remotes []string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Archive new or changed sessions from all agents",
		Long: `Scans every installed agent for new or changed sessions and archives them.
Incremental and idempotent: unchanged sessions are skipped by checksum.

With --remote, session files are first mirrored from SSH-reachable machines
(plain ssh + tar; nothing to install remotely) and archived alongside local
ones. Remote sessions carry a "host" meta field.`,
		Example: `  tape sync
  tape sync --since 7d
  tape sync --remote dev@build-server --remote user@10.0.0.7`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			ix, err := app.Index()
			if err != nil {
				return err
			}

			sources := app.Sources
			for _, host := range remotes {
				if app.SourceFactory == nil {
					return fmt.Errorf("remote sync not wired (no source factory)")
				}
				m := &remote.Mirror{Host: host, Dir: remote.HostDir(app.dir, host)}
				if !app.useJSON() {
					fmt.Fprintf(os.Stderr, "mirroring %s…\n", host)
				}
				if err := m.Pull(cmd.Context()); err != nil {
					return fmt.Errorf("remote %s: %w", host, err)
				}
				for _, src := range app.SourceFactory(m.Dir) {
					sources = append(sources, remote.WrapSource(src, host))
				}
			}

			sync := &service.Sync{Sources: sources, Archive: app.Archive(), Index: ix}
			report, err := sync.Run(cmd.Context(), t)
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(map[string]any{
					"sources":  report.Sources,
					"archived": report.Archived(),
					"skipped":  report.Skipped(),
					"errors":   report.ErrorCount(),
				})
			}
			for _, s := range report.Sources {
				label := s.Agent
				if s.Host != "" {
					label = s.Agent + "@" + s.Host
				}
				if !s.Found {
					fmt.Printf("%-12s not found\n", label)
					continue
				}
				fmt.Printf("%-12s scanned %-4d archived %-4d skipped %-4d", label, s.Scanned, s.Archived, s.Skipped)
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
	cmd.Flags().StringArrayVar(&remotes, "remote", nil, "also sync agent sessions from an SSH host (repeatable)")
	return cmd
}
