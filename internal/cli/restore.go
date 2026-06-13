package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/llm"
	"github.com/chenhg5/tape/internal/restore"
)

// firstAgent picks a sensible target agent for the suggestion in error
// messages. We use whatever the user already set with --to, falling back
// to "codex" because it's the lowest-friction destination (works without
// a browser login).
func firstAgent(to string) string {
	if to != "" {
		return to
	}
	return "codex"
}

func newRestoreCmd(app *App) *cobra.Command {
	var to, strategy, output, llmName, dir string
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "restore [session-id]",
		Aliases: []string{"rewind"},
		Short:   "Continue an archived session in another agent",
		Long: `Restores an archived session so a different agent can pick up the work.

Run with no arguments on a TTY to pick the session, target agent and
strategy from a menu. The menu is scoped to the current directory by
default (override with --dir / pass --dir "" for "all sessions").
Agents and piped invocations must always supply <session-id> and --to,
so scripts stay deterministic.

Strategies (ordered by fidelity, highest first):
  native      rewrite the dialogue as a real session of the target agent and
              resume it with the agent's own command (claude-code, codex)
  memory      write a transcript and @-reference it from the target agent's
              project memory file (CLAUDE.md / AGENTS.md) so it auto-loads
  transcript  write the full verbatim conversation as markdown; the agent
              reads the file when you ask it to
  brief       LLM-condensed handoff (goal / state / files / next); smallest
              payload, requires a local agent CLI (claude/codex/cursor-agent)

Default strategy is native when the target supports it, memory otherwise.`,
		Example: `  tape restore                                        # interactive on a TTY
  tape restore claude-code/7dd2afaf --to codex
  tape restore codex/019ea0af --to claude-code --strategy brief
  tape restore @last --to codex --dry-run`,
		// Allow 0 args (interactive); refuse > 1 (typo / shell glob).
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageErrf("restore takes at most one session id (got %d). "+
					"Quote the id if it contains spaces.", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var id string
			// Interactive path: no id and no --to, on a TTY. Build both
			// inputs from a menu rather than parroting cobra-style errors
			// at the user; agents never hit this branch (they pipe).
			if len(args) == 0 && to == "" {
				if !app.interactive() {
					return usageErrf("missing session id. try:\n" +
						"  tape restore @last --to codex            (most recent session)\n" +
						"  tape restore <id-fragment> --to codex    (e.g. 7dd2afaf)\n" +
						"run 'tape ls' to see available sessions")
				}
				// --dir untouched ⇒ scope to cwd (most common ask).
				// --dir "" ⇒ explicit "search every project".
				// --dir <path> ⇒ that exact path.
				var scope string
				if cmd.Flag("dir").Changed {
					scope = resolveDirFilter(dir)
				} else if wd, err := os.Getwd(); err == nil {
					scope = wd
				}
				sid, target, strat, err := pickRestore(cmd.Context(), app, scope)
				if err != nil {
					if errors.Is(err, errPromptAborted) {
						return usageErrf("cancelled")
					}
					return err
				}
				id, to = sid, target
				// User explicitly picked a strategy in the menu — honor
				// it over the --strategy default ("auto"). If they did
				// pass --strategy on the CLI, respect that instead.
				if !cmd.Flag("strategy").Changed {
					strategy = strat
				}
			} else if len(args) == 0 {
				return usageErrf("missing session id. try:\n"+
					"  tape restore @last --to %s\n"+
					"  tape restore <id-fragment> --to %s", firstAgent(to), firstAgent(to))
			} else {
				id = args[0]
			}
			if to == "" {
				return usageErrf("--to <agent> is required. " +
					"Pick one of: claude-code | codex | cursor | opencode | " +
					"gemini | antigravity | qwen | iflow | qoder | mimocode | kimi-code | aider.\n" +
					"  e.g. tape restore @last --to codex")
			}
			full, err := app.resolveSessionID(cmd.Context(), id)
			if err != nil {
				if errors.Is(err, ErrNoResults) {
					return err
				}
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
				return usageErrf("unknown target agent %q (run `tape ls --json` to see registered agents)", to)
			}
			writer, canNative := target.(ports.SessionWriter)
			if strategy == "auto" {
				if canNative {
					strategy = "native"
				} else {
					// Prefer memory over brief in auto-mode: full
					// fidelity, zero LLM cost, agent picks it up
					// without being asked.
					strategy = "memory"
				}
			}
			if strategy == "native" && !canNative {
				return usageErrf("agent %q does not support native restore; try --strategy memory or transcript", to)
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
					app.lead()
					fmt.Printf("%s would restore %s (%d msg) to %s via %s\n",
						app.gray("·"),
						app.cyan(full),
						len(sess.Messages),
						app.agentColor(to),
						app.bold(strategy))
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
				app.lead()
				fmt.Printf("%s restored %s as a native %s session.\n%s\n\n  %s\n",
					app.green("✓"), app.cyan(full), app.agentColor(to),
					app.gray("Resume it with:"),
					app.bold(resumeCmd))
				return nil
			case "transcript":
				doc := restore.Transcript(sess)
				if err := os.WriteFile(output, []byte(doc), 0o600); err != nil {
					return err
				}
				start := startHint(to, output)
				if app.useJSON() {
					return emitJSON(map[string]any{
						"strategy": "transcript", "target": to,
						"source": full, "handoff_file": output, "start_command": start,
					})
				}
				app.lead()
				fmt.Printf("%s full transcript written to %s\n%s\n\n  %s\n",
					app.green("✓"), app.cyan(output),
					app.gray("Open the next agent and ask it to read the file, e.g.:"),
					app.bold(start))
				return nil
			case "memory":
				doc := restore.Transcript(sess)
				// projectRoot precedence:
				//   1. dir(--output) when --output is absolute — the user
				//      explicitly told us where to land
				//   2. sess.CWD when that directory still exists on this
				//      machine — usual case for "resume on same box"
				//   3. current working directory — last-resort fallback,
				//      keeps the command working in fresh environments
				projectRoot := ""
				if filepath.IsAbs(output) {
					projectRoot = filepath.Dir(output)
				} else if st, err := os.Stat(sess.CWD); err == nil && st.IsDir() {
					projectRoot = sess.CWD
				} else if wd, err := os.Getwd(); err == nil {
					projectRoot = wd
				}
				handoffAbs, memAbs, err := restore.InjectMemory(projectRoot, to, output, doc)
				if err != nil {
					return err
				}
				if app.useJSON() {
					return emitJSON(map[string]any{
						"strategy": "memory", "target": to, "source": full,
						"handoff_file": handoffAbs, "memory_file": memAbs,
						"project_root": projectRoot,
					})
				}
				app.lead()
				fmt.Printf("%s transcript: %s\n%s memory:     %s\n%s\n\n  %s\n",
					app.green("✓"), app.cyan(handoffAbs),
					app.green("✓"), app.cyan(memAbs),
					app.gray("Open the next agent inside "+projectRoot+"; it will auto-load the handoff."),
					app.bold(startInProject(to, projectRoot)))
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
				app.lead()
				fmt.Printf("%s handoff written to %s %s\n%s\n\n  %s\n",
					app.green("✓"), app.cyan(output), app.gray("("+method+")"),
					app.gray("Start the next agent with:"),
					app.bold(start))
				return nil
			default:
				return usageErrf("unknown strategy %q (native|memory|transcript|brief)", strategy)
			}
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target agent (required outside interactive mode); use `tape ls --json` to see archived agents")
	cmd.Flags().StringVar(&strategy, "strategy", "auto", "restore strategy: auto|native|memory|transcript|brief")
	cmd.Flags().StringVar(&output, "output", ".tape-handoff.md", "handoff file path (memory/transcript/brief)")
	cmd.Flags().StringVar(&llmName, "llm", "auto", "summarizer for brief: auto|claude|codex|cursor|none")
	cmd.Flags().StringVar(&dir, "dir", "", "scope the interactive menu to a project directory (default: cwd; pass \"\" for all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without writing (exit 10 on success)")
	return cmd
}

// agentLaunchBin returns the shell command used to start the named agent
// in an interactive session — used both for "open this handoff in agent X"
// hints and for the memory-strategy variant where the agent auto-loads
// the handoff from a CLAUDE.md / AGENTS.md / IFLOW.md file. The bool is
// false when the agent has no known launcher; callers then fall back to
// a generic prompt that doesn't assume a binary name.
func agentLaunchBin(agent string) (string, bool) {
	switch agent {
	case "claude-code":
		return "claude", true
	case "codex":
		return "codex", true
	case "cursor":
		return "cursor-agent", true
	case "gemini":
		return "gemini", true
	case "qwen":
		return "qwen", true
	case "iflow":
		return "iflow", true
	case "aider":
		return "aider", true
	case "opencode":
		return "opencode", true
	case "antigravity":
		return "agy", true
	case "qoder":
		return "qodercli", true
	case "mimocode":
		return "mimo", true
	case "kimi-code":
		return "kimi", true
	default:
		return agent, false
	}
}

func startHint(agent, handoff string) string {
	prompt := fmt.Sprintf("Read %s and continue the work described there", handoff)
	bin, ok := agentLaunchBin(agent)
	if !ok {
		return prompt
	}
	return fmt.Sprintf("%s %q", bin, prompt)
}

// startInProject is the memory-strategy variant of startHint: the agent
// auto-loads the handoff because we wrote it into the project's memory
// file, so the user just opens the agent inside the project root.
func startInProject(agent, projectRoot string) string {
	bin, _ := agentLaunchBin(agent)
	return fmt.Sprintf("cd %s && %s", projectRoot, bin)
}
