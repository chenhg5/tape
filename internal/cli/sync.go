package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/service"
	"github.com/chenhg5/tape/internal/remote"
)

// renderSyncReport prints one row per *installed* source, then a
// colored summary line. Layout:
//
//	✓ claude-code         scanned 38   archived  0   skipped 38
//	✓ codex@build-server  scanned 13   archived  1   skipped 12
//
// Agents we couldn't find on disk are intentionally omitted — users
// have no action they can take on "you didn't install qoder", and the
// list grows every time we add another adapter. The footer rolls them
// up as a single dim "(N agents not installed)" count so the
// information is still discoverable for the curious without dominating
// the report. The JSON payload (`tape sync --json`) keeps every source,
// including missing ones — that path is for diagnostics, not eyeballs.
//
// Leading glyphs: ✓ ok, ! errors, · scanned-but-no-new.
func renderSyncReport(app *App, report service.SyncReport) {
	app.lead()
	missing := 0
	for _, s := range report.Sources {
		if !s.Found {
			missing++
			continue
		}
		label := s.Agent
		if s.Host != "" {
			label = s.Agent + "@" + s.Host
		}
		labelW := 22
		labelCol := padRightDisp(label, labelW)
		colored := app.agentColor(s.Agent) + labelCol[len(s.Agent):]

		mark := app.green("✓")
		if len(s.Errors) > 0 {
			mark = app.red("!")
		} else if s.Archived == 0 && s.Scanned == 0 {
			mark = app.gray("·")
		}
		archivedCell := padRightDisp(fmt.Sprintf("%d", s.Archived), 4)
		if s.Archived > 0 {
			archivedCell = app.bold(app.green(fmt.Sprintf("%d", s.Archived))) + archivedCell[len(fmt.Sprintf("%d", s.Archived)):]
		}
		fmt.Printf("  %s %s  %s %s   %s %s   %s %s\n",
			mark, colored,
			app.gray("scanned"), padRightDisp(fmt.Sprintf("%d", s.Scanned), 4),
			app.gray("archived"), archivedCell,
			app.gray("skipped"), padRightDisp(fmt.Sprintf("%d", s.Skipped), 4))
		for _, e := range s.Errors {
			fmt.Fprintf(os.Stderr, "    %s %s\n", app.red("!"), e)
		}
	}
	a, sk := report.Archived(), report.Skipped()
	switch {
	case a > 0:
		fmt.Printf("\n%s %s",
			app.green("✓"),
			app.bold(fmt.Sprintf("%d new session(s) archived", a)))
	case sk > 0:
		fmt.Printf("\n%s %s", app.gray("·"), app.gray("nothing new"))
	default:
		fmt.Printf("\n%s %s", app.gray("·"), app.gray("no agent data found"))
	}
	if missing > 0 {
		fmt.Printf("  %s", app.gray(fmt.Sprintf("(%d agent(s) not installed)", missing)))
	}
	fmt.Println()
}

func newSyncCmd(app *App) *cobra.Command {
	var since string
	var remotes []string
	var full bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Archive new or changed sessions from all agents",
		Long: `Scans every installed agent for new or changed sessions and archives them.
Incremental and idempotent: unchanged sessions are skipped by checksum.

Use --full to bypass the staleness short-circuit and re-archive every
session from scratch — handy after a parser change, a corrupted archive,
or when a new IR field needs backfilling. The on-disk archive is
rewritten in place; nothing is deleted.

With --remote, session files are first mirrored from SSH-reachable machines
(plain ssh + tar; nothing to install remotely) and archived alongside local
ones. Remote sessions carry a "host" meta field, are tagged @host in
ls / search output, and resume over SSH automatically.`,
		Example: `  tape sync
  tape sync --since 7d
  tape sync --full
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

			// Transparent self-heal: if the on-disk index was built by an
			// older indexer (different tokenizer, no @meta row, no tool
			// outputs, etc.), wipe and rebuild it before adding any new
			// sessions. Users should never have to remember `tape index
			// rebuild` — `sync` makes the index match the archive.
			if err := app.maybeRebuildIndex(cmd, ix); err != nil {
				return err
			}

			sources := app.Sources
			for _, host := range remotes {
				if app.SourceFactory == nil {
					return fmt.Errorf("remote sync not wired (no source factory)")
				}
				m := &remote.Mirror{Host: host, Dir: remote.HostDir(app.home, host)}
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

			pb := app.newProgress("syncing", 0)
			sync := &service.Sync{
				Sources: sources, Archive: app.Archive(), Index: ix,
				Full: full,
				OnSourceStart: func(agent, host string, total int) {
					label := agent
					if host != "" {
						label = agent + "@" + host
					}
					pb.label = label
					pb.current = 0
					pb.SetTotal(int64(total))
				},
				OnSessionDone: func(_, _ string, done, _ int, id string) {
					pb.Update(int64(done), id)
				},
			}
			report, err := sync.Run(cmd.Context(), t)
			pb.Done("")
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
			renderSyncReport(app, report)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only scan sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().StringArrayVar(&remotes, "remote", nil, "also sync agent sessions from an SSH host (repeatable)")
	cmd.Flags().BoolVar(&full, "full", false, "re-archive every session, ignoring checksum staleness")
	return cmd
}
