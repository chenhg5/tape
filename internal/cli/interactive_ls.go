package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// runInteractiveLs is the TTY entry point of `tape ls`. It applies the
// same filter the table path uses, shows a session picker, then a small
// action menu (Resume / Show / Copy ID / Copy resume command).
//
// "Resume here" hands off the terminal via syscall.Exec — tape exits,
// the agent takes over the PTY. Every other action returns normally so
// the user is back at their shell prompt with what they asked for.
func runInteractiveLs(ctx context.Context, app *App, filter ports.Filter) error {
	sums, err := app.Archive().List(ctx, filter)
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		return ErrNoResults
	}
	pick, err := pickSessionFromSummaries(app, sums, "Pick a session:")
	if err != nil {
		if errors.Is(err, errPromptAborted) {
			return nil // quiet abort, exit 0
		}
		return err
	}
	full, err := app.Archive().Get(ctx, pick.ID)
	if err != nil {
		return err
	}
	return runSessionAction(app, full)
}

// pickSessionFromSummaries is the version of pickSession that works
// off an already-fetched summary list. The plain `pickSession` helper
// in prompt.go hard-codes its own filter (used by show/restore prompts);
// here we want the picker to honor whatever flags the ls user passed.
func pickSessionFromSummaries(app *App, sums []model.Summary, prompt string) (model.Summary, error) {
	labels := make([]string, len(sums))
	for i, s := range sums {
		title := s.Title
		if title == "" {
			title = s.Project
		}
		labels[i] = fmt.Sprintf("%s  %s  %s  %s",
			app.cyan(padRightDisp(shortID(s.ID), 22)),
			app.agentColor(padRightDisp(s.Agent, 11)),
			padRightDisp(relTime(s.UpdatedAt), 9),
			truncDisp(title, 50))
	}
	idx, err := pickInteractive(app, prompt, labels)
	if err != nil {
		return model.Summary{}, err
	}
	return sums[idx], nil
}

// sessionAction describes one entry in the post-pick action menu.
type sessionAction struct {
	key   string
	label string
	desc  string
}

// runSessionAction shows the action menu for a chosen session and
// dispatches the pick. All four actions are no-questions-asked once
// selected — keep the menu honest and the path-to-action short.
func runSessionAction(app *App, sess *model.Session) error {
	resumeCmd := resumeCmdline(sess)
	actions := []sessionAction{
		{"resume", "Resume here",
			"cd into the session's cwd and " + agentLaunchHint(sess.Agent, sess.SourceID)},
		{"show", "Show transcript",
			"pretty-print this session in the current terminal"},
		{"copy_id", "Copy session ID",
			"OSC 52 to clipboard (" + shortID(sess.ID) + ")"},
		{"copy_cmd", "Copy resume command",
			"OSC 52 to clipboard (" + resumeCmd + ")"},
	}
	labels := make([]string, len(actions))
	for i, a := range actions {
		labels[i] = fmt.Sprintf("%s  %s %s",
			app.bold(padRightDisp(a.label, 22)),
			app.gray("·"),
			app.gray(a.desc))
	}
	pick, err := pickInteractive(app, "What now?", labels)
	if err != nil {
		if errors.Is(err, errPromptAborted) {
			return nil
		}
		return err
	}
	switch actions[pick].key {
	case "resume":
		return resumeInPlace(app, sess)
	case "show":
		renderSession(app, sess, false)
		return nil
	case "copy_id":
		return copyAndNotify(app, sess.ID, "session ID")
	case "copy_cmd":
		return copyAndNotify(app, resumeCmd, "resume command")
	}
	return nil
}

// copyAndNotify pushes s onto the user's clipboard through whichever
// channel actually works on their host, then prints the same string
// in cyan as an audit / fallback line. The label `via <method>` makes
// the chosen channel visible — important when debugging "the clipboard
// didn't update", because the failure mode is almost always silent
// (the terminal dropped the OSC 52, tmux ate it, …).
func copyAndNotify(app *App, payload, what string) error {
	method := copyClipboard(payload)
	app.lead()
	fmt.Printf("  %s copied %s to clipboard %s\n",
		app.green("✓"), what, app.gray("("+method+")"))
	fmt.Printf("    %s %s\n", app.gray("·"), app.cyan(payload))
	return nil
}

// agentResumeArgs returns the CLI args that drop the agent straight
// into a specific archived session. Empty slice means "no native
// resume flag — just launch the agent and let its built-in /resume
// picker take over".
//
// Mapping is per published CLI docs as of 2026-06:
//
//	claude-code  claude --resume <uuid>
//	codex        codex resume <uuid>
//	antigravity  agy --conversation <uuid>
//	qoder        qodercli -r <session-id>
//	others       (no public resume flag at time of writing)
func agentResumeArgs(agent, sourceID string) []string {
	if sourceID == "" {
		return nil
	}
	switch agent {
	case "claude-code":
		return []string{"--resume", sourceID}
	case "codex":
		return []string{"resume", sourceID}
	case "antigravity":
		return []string{"--conversation", sourceID}
	case "qoder":
		return []string{"-r", sourceID}
	}
	return nil
}

// resumeCmdline renders the shell command a user would run to resume
// the session manually. Used by both the menu label and the
// "Copy resume command" action.
func resumeCmdline(sess *model.Session) string {
	bin, _ := agentLaunchBin(sess.Agent)
	parts := append([]string{bin}, agentResumeArgs(sess.Agent, sess.SourceID)...)
	return strings.Join(parts, " ")
}

// agentLaunchHint feeds the action menu description: explains whether
// the launch will land directly inside the conversation or in the
// agent's built-in picker.
func agentLaunchHint(agent, sourceID string) string {
	bin, _ := agentLaunchBin(agent)
	if len(agentResumeArgs(agent, sourceID)) > 0 {
		return "launch " + bin + " resumed inside this conversation"
	}
	return "launch " + bin + " (use its built-in /resume to pick up here)"
}

// resumeInPlace cd's into the session's cwd and execs the matching
// agent binary so the user lands inside the conversation with one
// keystroke. Best-effort cwd: if the directory no longer exists we
// just launch the agent wherever tape was invoked from.
func resumeInPlace(app *App, sess *model.Session) error {
	bin, ok := agentLaunchBin(sess.Agent)
	if !ok {
		return cliError{
			Type:       "no_launcher",
			Message:    fmt.Sprintf("don't know how to launch %s", sess.Agent),
			Suggestion: "use `tape restore` to write a transcript / memory file instead",
		}
	}
	binPath, err := exec.LookPath(bin)
	if err != nil {
		return cliError{
			Type:       "launcher_not_installed",
			Message:    fmt.Sprintf("%s not found on $PATH", bin),
			Suggestion: "install " + bin + " or `tape restore <id> --strategy memory`",
		}
	}
	cwd := ""
	if sess.CWD != "" {
		if st, err := os.Stat(sess.CWD); err == nil && st.IsDir() {
			if err := os.Chdir(sess.CWD); err == nil {
				cwd = sess.CWD
			}
		}
	}
	args := append([]string{bin}, agentResumeArgs(sess.Agent, sess.SourceID)...)
	app.lead()
	fmt.Fprintf(os.Stderr, "  %s exec %s\n", app.gray("·"), app.bold(strings.Join(args, " ")))
	if cwd != "" {
		fmt.Fprintf(os.Stderr, "    %s %s\n", app.gray("·"), app.cyan(cwd))
	} else if sess.CWD != "" {
		fmt.Fprintf(os.Stderr, "    %s original cwd %s no longer exists, launching here\n",
			app.gray("!"), app.cyan(sess.CWD))
	}
	return execAgent(binPath, args, os.Environ())
}

// copyClipboard tries several channels to land s on the user's system
// clipboard, returning a short label naming whichever one was used.
// SSH + tmux is the failure mode that motivated this multi-path
// approach: a naked OSC 52 escape gets eaten by tmux unless several
// non-default tmux options are set, so a single-channel "just write
// OSC 52 to stderr" implementation looks broken to most tmux users.
//
// Order (highest reliability first):
//
//  1. tmux load-buffer -w -   (requires tmux ≥ 3.2; the `-w` flag asks
//     tmux to also forward the buffer to the outer terminal's system
//     clipboard via the right passthrough framing. Best path for the
//     SSH + tmux case.)
//
//  2. tmux load-buffer -      (older tmux: lands in tmux's own buffer;
//     user pastes with prefix + `]`. Strictly worse than #1 but still
//     better than a silent OSC 52.)
//
//  3. Platform helpers — pbcopy / wl-copy / xclip / xsel / clip.exe.
//     First match on $PATH wins. clip.exe gets WSL users to the
//     Windows clipboard for free.
//
//  4. Raw OSC 52 to stderr.  Last-resort; the terminal can drop it
//     silently, which is exactly why we always also print the payload
//     so the user can triple-click out of scrollback.
func copyClipboard(s string) string {
	if os.Getenv("TMUX") != "" {
		if err := pipeToCmd("tmux", []string{"load-buffer", "-w", "-"}, s); err == nil {
			return "tmux load-buffer -w"
		}
		if err := pipeToCmd("tmux", []string{"load-buffer", "-"}, s); err == nil {
			return "tmux buffer · paste with prefix+]"
		}
	}
	for _, c := range clipboardHelpers() {
		if _, err := exec.LookPath(c.bin); err != nil {
			continue
		}
		if err := pipeToCmd(c.bin, c.args, s); err == nil {
			return c.bin
		}
	}
	emitOSC52(s)
	return "OSC 52 · may be dropped silently — fallback line below is selectable"
}

type clipHelper struct {
	bin  string
	args []string
}

// clipboardHelpers lists the per-OS shell commands we try in
// preference order. The ordering matters: on Linux we try Wayland's
// wl-copy before the X11 tools so a Sway/Hyprland user picks up the
// right backend; clip.exe is listed last because it only works in
// WSL, where xclip / xsel typically aren't installed anyway.
func clipboardHelpers() []clipHelper {
	switch runtime.GOOS {
	case "darwin":
		return []clipHelper{{"pbcopy", nil}}
	case "windows":
		return []clipHelper{{"clip", nil}}
	default: // linux + other unixes
		return []clipHelper{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
			{"xsel", []string{"--clipboard", "--input"}},
			{"clip.exe", nil}, // WSL → Windows host
		}
	}
}

func pipeToCmd(bin string, args []string, stdin string) error {
	c := exec.Command(bin, args...)
	c.Stdin = strings.NewReader(stdin)
	return c.Run()
}

// emitOSC52 writes the OSC 52 clipboard control sequence to stderr.
// Kept as a separate helper so the wire format stays trivial to unit-
// test even as copyClipboard's strategy stack evolves around it.
func emitOSC52(s string) {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\x07", enc)
}
