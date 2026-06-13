package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/service"
	"github.com/chenhg5/tape/internal/remote"
	"github.com/chenhg5/tape/internal/schedule"
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
	var install, uninstall, status bool
	var interval time.Duration
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
ls / search output, and resume over SSH automatically.

Scheduling: 'tape sync --install [--interval 1h]' wires a user-scoped
timer to run sync on a fixed cadence — systemd user timer on Linux,
LaunchAgent on macOS, or a schtasks one-liner on Windows (printed for
you to run). 'tape sync --uninstall' removes it; 'tape sync --status'
reports whether one is currently registered. The job runs as your
user, never root.`,
		Example: `  tape sync
  tape sync --since 7d
  tape sync --full
  tape sync --remote dev@build-server --remote user@10.0.0.7
  tape sync --install --interval 30m
  tape sync --status
  tape sync --uninstall`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if install || uninstall || status {
				return runSyncSchedule(cmd, app, install, uninstall, status, interval, remotes)
			}
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
	cmd.Flags().BoolVar(&install, "install", false, "install a periodic sync job (systemd timer / LaunchAgent / schtasks)")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove the periodic sync job")
	cmd.Flags().BoolVar(&status, "status", false, "report whether a periodic sync job is installed")
	cmd.Flags().DurationVar(&interval, "interval", time.Hour, "interval between scheduled syncs (used with --install)")
	return cmd
}

// runSyncSchedule dispatches the --install / --uninstall / --status
// modes. They all share the same underlying schedule package and
// the same JSON contract; keeping the modes here (instead of as
// sub-commands) means users don't have to learn `tape sync
// schedule install` etc. — just `tape sync --install`.
//
// --install + --uninstall together is a usage error; --status pairs
// fine with either but short-circuits to read-only.
func runSyncSchedule(cmd *cobra.Command, app *App, install, uninstall, status bool, interval time.Duration, remotes []string) error {
	if install && uninstall {
		return usageErrf("--install and --uninstall are mutually exclusive")
	}
	backend := schedule.Detect()

	if status {
		s, err := backend.Current()
		if err != nil {
			return err
		}
		return emitScheduleResult(app, "status", s, err)
	}
	if uninstall {
		s, err := backend.Uninstall()
		return emitScheduleResult(app, "uninstall", s, err)
	}

	// --install: figure out which binary to wire as the job.
	// os.Executable returns the running tape binary, which is
	// exactly what we want — if the user installed a beta and is
	// scheduling from it, they want the beta scheduled.
	exe, err := os.Executable()
	if err != nil {
		return cliError{Type: "schedule_failed", Message: "cannot resolve tape binary: " + err.Error()}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	args := []string{"sync"}
	for _, h := range remotes {
		args = append(args, "--remote", h)
	}
	job := schedule.Job{Binary: exe, Args: args, Interval: interval}
	s, err := backend.Install(job)
	return emitScheduleResult(app, "install", s, err)
}

// emitScheduleResult is the shared printer for the three schedule
// modes. We emit the same JSON envelope shape regardless of action;
// the human path stays terse (one or two lines).
func emitScheduleResult(app *App, action string, s schedule.Status, runErr error) error {
	if app.useJSON() {
		payload := map[string]any{"action": action, "status": s}
		if runErr != nil {
			payload["error"] = runErr.Error()
		}
		if err := emitJSON(payload); err != nil {
			return err
		}
		return runErr
	}
	app.lead()
	mark := app.green("✓")
	if runErr != nil {
		mark = app.yellow("!")
	}
	fmt.Printf("  %s %s (%s)\n", mark, app.bold(action), s.Backend)
	if s.Path != "" {
		fmt.Printf("  %s path %s\n", app.gray("·"), s.Path)
	}
	if s.Interval != "" {
		fmt.Printf("  %s every %s\n", app.gray("·"), s.Interval)
	}
	if s.Detail != "" {
		fmt.Printf("  %s %s\n", app.gray("·"), s.Detail)
	}
	if action == "status" && !s.Installed && runErr == nil {
		fmt.Printf("  %s no scheduled sync; run `tape sync --install` to set one up\n", app.gray("·"))
	}
	return runErr
}
