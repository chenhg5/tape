package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/export/snapshot"
	"github.com/chenhg5/tape/internal/redact"
)

func (a *App) archiveDir() string { return filepath.Join(a.home, "archive") }

// newExportCmd builds `tape export`: one command, one job — write a
// snapshot of the archive (or a filter-scoped slice of it) to a
// single file. We deliberately don't ship a `restore` companion or a
// `push --to <provider>` sub-tree: any tarball / zip is trivially
// inspectable with system tools, and "back up to cloud X" is the
// user's choice of cloud, not tape's job.
//
// --scan-only is the only non-write mode: it reuses the same
// walk + filter pipeline so "what would I be exporting?" stays
// consistent with "what was exported".
func newExportCmd(app *App) *cobra.Command {
	var (
		output, agent, dir, since, host string
		format, compress                string
		noRedact, dryRun, scanOnly      bool
	)
	cmd := &cobra.Command{
		Use:   "export [output]",
		Short: "Export the archive (or a filtered slice) as a single artifact",
		Long: `Writes a snapshot of the archive — full, or scoped by --agent / --dir
/ --host / --since — to one file. Choose the container with --format
(tar or zip) and the codec with --compress (zstd, gzip, xz, none).
Defaults are tar + zstd, which is the smallest + fastest combination.

Secrets in session.json are redacted on the way into the artifact
(replaced with [REDACTED:<rule>] for text, length-preserving masks
for binaries). Local archive files are never modified. Pass
--no-redact if you actually want the raw bytes — useful for round-
tripping into another tape install you fully control.

Pass --scan-only to list what would be redacted, without writing
anything. Same filter, same walk — handy for auditing.

Importing is not a tape feature: any tar.zst / .tar.gz / .zip is
extractable with system tools, and a future tape can pick up the
resulting files exactly where it left them.`,
		Example: `  tape export                                    # everything → tape-export-<ts>.tar.zst
  tape export snapshot.tar.gz --compress gzip    # explicit name + codec
  tape export --agent codex --since 7d           # only codex, last week
  tape export --dir . --format zip --compress none  # current project as a .zip
  tape export --scan-only --agent claude-code    # audit secrets in claude sessions`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if output != "" {
					return usageErrf("specify the output either positionally or with --output, not both")
				}
				output = args[0]
			}
			if format == "" {
				format = snapshot.FormatTar
			}
			if compress == "" && format == snapshot.FormatTar {
				compress = snapshot.CompressZstd
			}
			if err := snapshot.ValidateFormat(format, compress); err != nil {
				return usageErrf("%v", err)
			}
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
			filter := ports.Filter{
				Agent: agent, Project: resolveDirFilter(dir),
				Host: host, Since: sinceTime,
			}

			if scanOnly {
				return runExportScan(cmd, app, filter)
			}

			if output == "" {
				output = snapshot.DefaultFilename(format, compress, time.Now())
			}

			pb := app.newProgress("export", 0)
			opts := ports.ExportOpts{
				ArchiveDir: app.archiveDir(),
				Output:     output,
				Filter:     filter,
				Format:     format,
				Compress:   compress,
				DryRun:     dryRun,
				OnProgress: func(done, total int64, path string) {
					if pb.total != total {
						pb.SetTotal(total)
					}
					pb.Update(done, path)
				},
			}
			if !noRedact {
				opts.RedactCopy = redactArtifact
			}
			res, err := snapshot.Write(cmd.Context(), app.Archive(), opts)
			pb.Done("")
			if err != nil {
				return err
			}
			if err := printExportResult(app, res); err != nil {
				return err
			}
			if dryRun {
				return errDryRun
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: tape-export-<timestamp>.<ext>)")
	cmd.Flags().StringVar(&format, "format", "", "container format: tar (default) or zip")
	cmd.Flags().StringVar(&compress, "compress", "", "codec: zstd (default for tar), gzip, xz, none")
	cmd.Flags().StringVar(&agent, "agent", "", "only export sessions from this agent")
	cmd.Flags().StringVar(&dir, "dir", "", "only export sessions under this working dir ('.' = cwd)")
	cmd.Flags().StringVar(&host, "host", "", `only export from this origin host ("local" or ssh-host)`)
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().BoolVar(&noRedact, "no-redact", false, "keep secrets verbatim in the artifact")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing (exit 10 on success)")
	cmd.Flags().BoolVar(&scanOnly, "scan-only", false, "list secrets that would be redacted, write nothing")
	return cmd
}

// redactArtifact is the default mask function: length-preserving on
// the SQLite DB (so the file structure isn't broken) and readable
// markers everywhere else. Same policy the old `tape backup export`
// applied; lifted into its own function so the export pipeline and
// any future caller can reuse it without dragging in a Backup type.
func redactArtifact(path string, data []byte) []byte {
	if filepath.Ext(path) == ".db" {
		return redact.ApplyKeepLength(data)
	}
	return redact.Apply(data)
}

// runExportScan implements --scan-only: walk the archive scoped by
// the user's filter, return the secret findings without writing
// anything. We don't fail-exit on findings; this is an *audit* mode,
// not a gate. The real protection is the default redactor that runs
// on every actual export.
func runExportScan(cmd *cobra.Command, app *App, filter ports.Filter) error {
	findings, err := snapshot.Scan(cmd.Context(), app.Archive(),
		ports.ExportOpts{ArchiveDir: app.archiveDir(), Filter: filter},
		func(path string, data []byte) []snapshot.Finding {
			rs := redact.Scan(path, data)
			out := make([]snapshot.Finding, len(rs))
			for i, f := range rs {
				out[i] = snapshot.Finding{
					Rule: f.Rule, Path: f.Path, Line: f.Line, Preview: f.Preview,
				}
			}
			return out
		})
	if err != nil {
		return err
	}
	if app.useJSON() {
		return emitJSON(map[string]any{
			"findings": findings, "count": len(findings),
		})
	}
	app.lead()
	if len(findings) == 0 {
		fmt.Printf("%s no secrets found in scope; export would carry redactions for: nothing\n", app.green("✓"))
		return nil
	}
	fmt.Printf("%s %s\n",
		app.yellow("!"),
		app.bold(fmt.Sprintf("%d potential secret(s) in scope (would be redacted on export):", len(findings))))
	for _, f := range findings {
		fmt.Printf("  %s %s:%d  %s\n",
			app.yellow(padRightDisp(f.Rule, 18)),
			app.cyan(f.Path),
			f.Line,
			app.gray(truncDisp(collapseWhitespace(f.Preview), 60)))
	}
	return nil
}

func printExportResult(app *App, res *ports.ExportResult) error {
	if app.useJSON() {
		return emitJSON(map[string]any{"result": res})
	}
	app.lead()
	fmt.Printf("%s %s %s",
		app.green("✓"),
		app.cyan(res.Output),
		app.bold(fmt.Sprintf("· %d file(s)", res.Changed)))
	if res.Bytes > 0 {
		fmt.Printf("  %s", app.gray(humanSize(res.Bytes)))
	}
	if res.Note != "" {
		fmt.Printf("\n  %s", app.gray(res.Note))
	}
	fmt.Println()
	return nil
}

// humanSize renders an int64 byte count as a short human string.
// We don't pull in a unit library for this — IEC powers of two with
// a single-decimal cut is what every "ls -lh" style expects.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
