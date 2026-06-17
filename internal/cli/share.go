package cli

import (
	"github.com/spf13/cobra"
)

// newShareCmd is a thin alias over `tape export --session <id> --kind share`.
// We keep it as its own cobra.Command (not a runtime alias) so help text,
// completions and JSON `command` records read naturally — "share" is the
// verb most users reach for when they want to hand a chat to someone else.
//
// The bundle still lands in the current directory (not ~/.tape/exports)
// because the share workflow is "create artifact → upload / scp →
// delete", not "keep around for history".
func newShareCmd(app *App) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "share <session-id> [...]",
		Short: "Export one or more sessions as a shareable bundle (alias for `export --session ... --kind share`)",
		Long: `Bundles one or more archived sessions into a single artifact intended
for handing off to another person or machine. The bundle includes a
tape-bundle.json manifest so the receiving tape can import it with
session-level metadata intact.

This is a convenience wrapper around 'tape export': for finer
control (chunking, agent filters, output paths, format) reach for
'tape export --session <id> --kind share' directly.`,
		Example: `  tape share @last                     # most recent session, default name
  tape share claude-code/abc123        # one specific session
  tape share @last,codex/xyz           # multiple in one bundle
  tape share @last -o ./my-share.tar.zst`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Translate `tape share a b c` and `tape share a,b,c` into the
			// underlying `--session a,b,c` form. Doing it here keeps the
			// help line obvious ("<session-id> [...]") even though export
			// takes a comma-separated string.
			sel := joinSessionArgs(args)
			exportCmd := newExportCmd(app)
			passthrough := []string{
				"--session", sel,
				"--kind", "share",
			}
			if output != "" {
				passthrough = append(passthrough, "-o", output)
			}
			exportCmd.SetArgs(passthrough)
			exportCmd.SetContext(cmd.Context())
			return exportCmd.Execute()
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "bundle file path (default: tape-share-<agent>-<short_id>.tar.zst)")
	return cmd
}

// joinSessionArgs accepts the per-arg-or-comma-separated mix users
// reach for in practice and always emits a single comma-separated
// list for the export command. `share a b` and `share a,b` are
// indistinguishable downstream.
func joinSessionArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) == 1 {
		return args[0]
	}
	out := args[0]
	for _, a := range args[1:] {
		out += "," + a
	}
	return out
}
