package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/llm"
	"github.com/chenhg5/tape/internal/restore"
)

func newRestoreCmd(app *App) *cobra.Command {
	var to, strategy, output, llmName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "restore <session-id>",
		Aliases: []string{"rewind"},
		Short:   "Continue an archived session in another agent",
		Long: `Restores an archived session so a different agent can pick up the work.

Strategies:
  native  rewrite the dialogue as a native session of the target agent and
          resume it with the agent's own resume command (claude-code, codex)
  brief   generate a handoff document (any target; uses a local agent CLI as
          the summarizer when available, falls back to a template)

Default strategy is native when the target supports it, brief otherwise.`,
		Example: `  tape restore claude-code/7dd2afaf --to codex
  tape restore codex/019ea0af --to claude-code --strategy brief
  tape restore @last --to codex --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return usageErrf("--to is required (claude-code|codex|cursor)")
			}
			id := args[0]
			if id == "@last" {
				sums, err := app.Archive().List(cmd.Context(), ports.Filter{Limit: 1})
				if err != nil {
					return err
				}
				if len(sums) == 0 {
					return ErrNoResults
				}
				id = sums[0].ID
			}
			full, err := app.Archive().Resolve(cmd.Context(), id)
			if err != nil {
				return cliError{Type: "not_found", Message: err.Error(), Suggestion: "tape ls"}
			}
			sess, err := app.Archive().Get(cmd.Context(), full)
			if err != nil {
				return err
			}

			var target ports.Source
			for _, s := range app.Sources {
				if s.Name() == to {
					target = s
				}
			}
			if target == nil {
				return usageErrf("unknown target agent %q (claude-code|codex|cursor)", to)
			}
			writer, canNative := target.(ports.SessionWriter)
			if strategy == "auto" {
				if canNative {
					strategy = "native"
				} else {
					strategy = "brief"
				}
			}
			if strategy == "native" && !canNative {
				return usageErrf("agent %q does not support native restore; use --strategy brief", to)
			}

			if dryRun {
				plan := map[string]any{
					"session": sess.Summary(), "target": to, "strategy": strategy,
					"messages": len(sess.Messages),
				}
				if app.useJSON() {
					if err := emitJSON(plan); err != nil {
						return err
					}
				} else {
					fmt.Printf("would restore %s (%d messages) to %s via %s\n", full, len(sess.Messages), to, strategy)
				}
				return errDryRun
			}

			switch strategy {
			case "native":
				resumeCmd, err := writer.Write(cmd.Context(), sess)
				if err != nil {
					return err
				}
				if app.useJSON() {
					return emitJSON(map[string]any{
						"strategy": "native", "target": to, "source": full, "resume_command": resumeCmd,
					})
				}
				fmt.Printf("restored %s as a native %s session.\nResume it with:\n\n  %s\n", full, to, app.bold(resumeCmd))
				return nil
			case "brief":
				runner, err := llm.Pick(llmName)
				if err != nil {
					return err
				}
				if runner != nil && !app.useJSON() {
					fmt.Fprintf(os.Stderr, "summarizing via %s CLI…\n", runner.Name())
				}
				doc, method, err := restore.Brief(cmd.Context(), sess, runner)
				if err != nil {
					return err
				}
				if err := os.WriteFile(output, []byte(doc), 0o600); err != nil {
					return err
				}
				start := startHint(to, output)
				if app.useJSON() {
					return emitJSON(map[string]any{
						"strategy": "brief", "method": method, "target": to,
						"source": full, "handoff_file": output, "start_command": start,
					})
				}
				fmt.Printf("handoff written to %s (%s).\nStart the next agent with:\n\n  %s\n", output, method, app.bold(start))
				return nil
			default:
				return usageErrf("unknown strategy %q (native|brief)", strategy)
			}
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target agent: claude-code|codex|cursor (required)")
	cmd.Flags().StringVar(&strategy, "strategy", "auto", "restore strategy: auto|native|brief")
	cmd.Flags().StringVar(&output, "output", ".tape-handoff.md", "handoff file path (brief strategy)")
	cmd.Flags().StringVar(&llmName, "llm", "auto", "summarizer for brief: auto|claude|codex|cursor|none")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without writing (exit 10 on success)")
	return cmd
}

func startHint(agent, handoff string) string {
	prompt := fmt.Sprintf("Read %s and continue the work described there", handoff)
	switch agent {
	case "claude-code":
		return fmt.Sprintf("claude %q", prompt)
	case "codex":
		return fmt.Sprintf("codex %q", prompt)
	case "cursor":
		return fmt.Sprintf("cursor-agent %q", prompt)
	default:
		return prompt
	}
}
