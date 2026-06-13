package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newCompletionCmd emits the appropriate shell completion script
// for bash / zsh / fish / powershell. We don't try to install
// anything (writing into /etc, /usr/share, ~/.zshrc would be
// hostile); the script goes to stdout and the man-page-style help
// shows the per-shell install command users can copy.
//
// Cobra handles the script generation entirely; this command is
// the obvious user-facing surface for `tape completion zsh > …`
// and the place we can give per-shell install instructions in one
// place instead of duplicating them in three READMEs.
func newCompletionCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "Generate shell completion script",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Long: `Generates a completion script for the chosen shell and prints it on
stdout. Pipe it into the right place for your shell:

  Bash:
    # Linux (system-wide; needs sudo):
    tape completion bash | sudo tee /etc/bash_completion.d/tape >/dev/null
    # Per-user (load from your .bashrc):
    tape completion bash > ~/.local/share/bash-completion/completions/tape

  Zsh:
    # Per-user — pick a directory that's on $fpath (run 'echo $fpath' to check):
    tape completion zsh > "${fpath[1]}/_tape"
    # Then in ~/.zshrc, ensure 'autoload -U compinit && compinit' runs.

  Fish:
    tape completion fish > ~/.config/fish/completions/tape.fish

  PowerShell:
    tape completion powershell | Out-String | Invoke-Expression
    # Add the same line to your $PROFILE to make it permanent.

The script tab-completes subcommands, flags, agent shorthands, and
session IDs (where the command takes one). Update it after upgrading
tape so new commands and flags show up.`,
		Example: `  tape completion zsh > "${fpath[1]}/_tape"
  source <(tape completion bash)
  tape completion fish | source`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return fmt.Errorf("unknown shell %q", args[0])
		},
	}
	_ = app // currently unused but the signature matches the rest
	return cmd
}
