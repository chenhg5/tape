package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/backup/gitrepo"
	"github.com/chenhg5/tape/internal/backup/tarball"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/redact"
)

func (a *App) archiveDir() string { return filepath.Join(a.dir, "archive") }

func newBackupCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the archive (git, tarball)",
	}
	cmd.AddCommand(
		newBackupPushCmd(app),
		newBackupPullCmd(app),
		newBackupExportCmd(app),
		newBackupScanCmd(app),
	)
	return cmd
}

func newBackupPushCmd(app *App) *cobra.Command {
	var remote, message string
	var dryRun, allowSecrets bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Commit the archive to git and push to origin if set",
		Long: `Turns the archive directory into a git repository, commits all changes and
pushes when a remote is configured. The remote is stored in the repo itself,
so --remote is only needed once.

Secrets are scanned before anything leaves the machine; findings block the
push unless --allow-secrets is set (use 'tape backup export' for a redacted
artifact instead).`,
		Example: `  tape backup push --remote git@github.com:you/tape-archive.git
  tape backup push --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			findings, err := scanArchive(app.archiveDir())
			if err != nil {
				return err
			}
			if len(findings) > 0 && !allowSecrets {
				printFindings(app, findings)
				return cliError{
					Type:       "secrets_found",
					Message:    fmt.Sprintf("%d potential secret(s) in the archive; push blocked", len(findings)),
					Suggestion: "review findings, then re-run with --allow-secrets, or use `tape backup export` (redacted)",
				}
			}
			res, err := gitrepo.Target{}.Push(cmd.Context(), ports.BackupOpts{
				ArchiveDir:  app.archiveDir(),
				Destination: remote,
				Message:     message,
				DryRun:      dryRun,
			})
			if err != nil {
				return err
			}
			if err := printResult(app, res, findings); err != nil {
				return err
			}
			if dryRun {
				return errDryRun
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "git remote URL (persisted as origin)")
	cmd.Flags().StringVar(&message, "message", "", "commit message")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without committing (exit 10 on success)")
	cmd.Flags().BoolVar(&allowSecrets, "allow-secrets", false, "push even when the secret scan has findings")
	return cmd
}

func newBackupPullCmd(app *App) *cobra.Command {
	var remote string
	cmd := &cobra.Command{
		Use:     "pull",
		Short:   "Pull (or clone) the archive from git and rebuild the index",
		Example: "  tape backup pull --remote git@github.com:you/tape-archive.git",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := gitrepo.Target{}.Pull(cmd.Context(), ports.BackupOpts{
				ArchiveDir:  app.archiveDir(),
				Destination: remote,
			})
			if err != nil {
				return err
			}
			n, err := app.rebuildIndex(cmd)
			if err != nil {
				return err
			}
			res.Note = fmt.Sprintf("%s; reindexed %d session(s)", res.Note, n)
			return printResult(app, res, nil)
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "git remote URL (needed on a fresh machine)")
	return cmd
}

func newBackupExportCmd(app *App) *cobra.Command {
	var output string
	var noRedact, dryRun bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the archive as a redacted .tar.zst artifact",
		Long: `Writes a compressed snapshot of the archive. Secrets are replaced with
[REDACTED:<rule>] inside the artifact; local archive files are never touched.`,
		Example: "  tape backup export --output tape-archive.tar.zst",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := ports.BackupOpts{
				ArchiveDir:  app.archiveDir(),
				Destination: output,
				DryRun:      dryRun,
			}
			if !noRedact {
				opts.RedactCopy = func(path string, data []byte) []byte {
					// length-preserving masking for binary formats (sqlite),
					// readable markers for text
					if filepath.Ext(path) == ".db" {
						return redact.ApplyKeepLength(data)
					}
					return redact.Apply(data)
				}
			}
			res, err := tarball.Target{}.Push(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if err := printResult(app, res, nil); err != nil {
				return err
			}
			if dryRun {
				return errDryRun
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "tape-archive.tar.zst", "output file path")
	cmd.Flags().BoolVar(&noRedact, "no-redact", false, "keep secrets verbatim in the artifact")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing (exit 10 on success)")
	return cmd
}

func newBackupScanCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "scan",
		Short:   "Scan the archive for potential secrets",
		Example: "  tape backup scan --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			findings, err := scanArchive(app.archiveDir())
			if err != nil {
				return err
			}
			if app.useJSON() {
				if err := emitJSON(map[string]any{"findings": findings, "count": len(findings)}); err != nil {
					return err
				}
				if len(findings) == 0 {
					return nil
				}
				return cliError{Type: "secrets_found",
					Message: fmt.Sprintf("%d potential secret(s) found", len(findings))}
			}
			if len(findings) == 0 {
				fmt.Println("no secrets found")
				return nil
			}
			printFindings(app, findings)
			return cliError{Type: "secrets_found",
				Message: fmt.Sprintf("%d potential secret(s) found", len(findings))}
		},
	}
}

// scanArchive checks normalized session files. Raw files are skipped: they
// duplicate session.json content (plus vendor noise) and triple scan time,
// while session.json is what both backup paths actually expose.
func scanArchive(dir string) ([]redact.Finding, error) {
	var findings []redact.Finding
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "raw") {
				return filepath.SkipDir
			}
			return err
		}
		if filepath.Base(path) != "session.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		findings = append(findings, redact.Scan(rel, data)...)
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	return findings, err
}

func printFindings(app *App, findings []redact.Finding) {
	if app.useJSON() {
		return // findings travel inside the JSON error / result payloads
	}
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %s %s:%d %s\n", app.yellow(f.Rule), f.Path, f.Line, f.Preview)
	}
}

func printResult(app *App, res *ports.BackupResult, findings []redact.Finding) error {
	if app.useJSON() {
		payload := map[string]any{"result": res}
		if len(findings) > 0 {
			payload["findings"] = findings
		}
		return emitJSON(payload)
	}
	fmt.Printf("%s %s: %d file(s)", res.Target, res.Action, res.Changed)
	if res.Ref != "" {
		fmt.Printf(" @ %s", res.Ref)
	}
	if res.Note != "" {
		fmt.Printf(" (%s)", res.Note)
	}
	fmt.Println()
	return nil
}
