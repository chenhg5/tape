package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// errPromptAborted is returned when the user hits Ctrl-D or types "q" at a
// menu. Callers translate it into a clean usage error (exit 2) instead of
// a stack-trace-y error path.
var errPromptAborted = errors.New("aborted by user")

// promptChoice shows a numbered menu and returns the picked index (0-based).
// Empty input picks the first item; "q" / EOF aborts. We deliberately keep
// the contract dead simple — no arrow keys, no fuzzy search — because:
//   - it works on any terminal, including dumb ones and CI
//   - no extra dependency on a TUI library
//   - the numbered shortcut is just as fast as arrow nav for ≤ 20 items.
func promptChoice(app *App, title string, items []string) (int, error) {
	return promptChoiceR(app, os.Stdin, title, items)
}

// promptChoiceR is the testable seam: pass any io.Reader so unit tests can
// drive the wizard without poking a real TTY.
func promptChoiceR(app *App, in io.Reader, title string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, fmt.Errorf("%s: nothing to choose from", title)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, app.bold(title))
	width := len(strconv.Itoa(len(items)))
	for i, it := range items {
		idx := strconv.Itoa(i + 1)
		fmt.Fprintf(os.Stderr, "  %s%s  %s\n",
			strings.Repeat(" ", width-len(idx)),
			app.cyan(idx), it)
	}
	fmt.Fprintf(os.Stderr, "\n  %s ", app.gray(fmt.Sprintf("[1-%d, q to quit]>", len(items))))

	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return 0, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, nil // ENTER = first item; a "pick the obvious default" affordance.
	}
	if line == "q" || line == "Q" {
		return 0, errPromptAborted
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		return 0, fmt.Errorf("invalid selection %q (want 1-%d)", line, len(items))
	}
	return n - 1, nil
}

// pickSession asks the user to choose one archived session from a menu.
// dir scopes the menu to a project directory ("" = all sessions). prompt
// is the menu title — callers tailor it ("Pick a session to show:" vs
// "Pick a session to restore:") so the affordance matches their intent.
// Caller must guarantee app.interactive() first.
func pickSession(ctx context.Context, app *App, dir, prompt string) (model.Summary, error) {
	// Show up to 30 most recent sessions: fits a normal terminal and
	// still gives some depth for picking older work.
	sums, err := app.Archive().List(ctx, ports.Filter{Project: dir, Limit: 30})
	if err != nil {
		return model.Summary{}, err
	}
	if len(sums) == 0 {
		hint := "run 'tape sync' to archive your latest sessions first"
		if dir != "" {
			hint = fmt.Sprintf("no archived sessions under %s; try without --dir or run 'tape sync'", dir)
		}
		return model.Summary{}, cliError{Type: "no_results", Message: "no sessions found", Suggestion: hint}
	}

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
	if dir != "" {
		fmt.Fprintf(os.Stderr, "%s sessions under %s\n", app.gray("·"), app.cyan(dir))
	}
	pick, err := pickInteractive(app, prompt, labels)
	if err != nil {
		return model.Summary{}, err
	}
	return sums[pick], nil
}

// strategyChoice describes one entry in the strategy picker.
type strategyChoice struct {
	key   string // value passed to --strategy / RunE
	label string // bold first column
	desc  string // dim follow-up sentence
}

// strategyChoicesFor returns the menu items appropriate for target. We
// hide "native" when the target has no SessionWriter; we always offer
// the other three, ordered by fidelity (highest first).
func strategyChoicesFor(target string) []strategyChoice {
	out := []strategyChoice{}
	// claude-code and codex have SessionWriter; cursor doesn't (yet).
	if target == "claude-code" || target == "codex" {
		out = append(out, strategyChoice{
			key: "native", label: "Native resume",
			desc: "rewrite as a real " + target + " session, resume with the agent's own command",
		})
	}
	out = append(out,
		strategyChoice{key: "memory", label: "Memory injection",
			desc: "drop a transcript file and @-reference it from " + memoryFileHint(target) + " so the agent auto-loads it"},
		strategyChoice{key: "transcript", label: "Transcript document",
			desc: "write the full verbatim conversation to a markdown file; agent reads it on demand"},
		strategyChoice{key: "brief", label: "Brief summary",
			desc: "LLM-condensed handoff (goal / state / files / next); smallest payload"},
	)
	return out
}

// memoryFileHint mirrors restore.memoryFile() for the menu description;
// kept local to avoid a circular dep into the restore package for one
// string.
func memoryFileHint(target string) string {
	switch target {
	case "claude-code":
		return "CLAUDE.md"
	case "codex", "cursor", "opencode":
		return "AGENTS.md"
	case "gemini":
		return "GEMINI.md"
	case "qwen":
		return "QWEN.md"
	case "iflow":
		return "IFLOW.md"
	case "aider":
		return "CONVENTIONS.md"
	default:
		return "the project memory file"
	}
}

// pickRestore builds on pickSession with two more menus:
//
//  1. target agent (excluding the source agent — restoring to yourself
//     is almost always a mistake; you'd just re-run the original);
//  2. strategy — what shape of context to hand over.
//
// The third step gives the user real control instead of silently
// summarizing via the LLM the moment a session is picked.
func pickRestore(ctx context.Context, app *App, dir string) (sessionID, target, strategy string, err error) {
	src, err := pickSession(ctx, app, dir, "Pick a session to restore:")
	if err != nil {
		return "", "", "", err
	}
	var targets []string
	// Roughly ordered by global install share (claude-code/codex/cursor
	// first, then Gemini-family, then aider). Sticking to a fixed order
	// keeps muscle memory predictable across runs.
	for _, a := range []string{
		"claude-code", "codex", "cursor", "opencode",
		"gemini", "qwen", "iflow", "aider",
	} {
		if a != src.Agent {
			targets = append(targets, a)
		}
	}
	tlabels := make([]string, len(targets))
	for i, t := range targets {
		tlabels[i] = app.agentColor(t)
	}
	tpick, err := pickInteractive(app, "Restore to which agent?", tlabels)
	if err != nil {
		return "", "", "", err
	}
	target = targets[tpick]

	choices := strategyChoicesFor(target)
	slabels := make([]string, len(choices))
	for i, c := range choices {
		// Two-column layout: bold label, a "·" separator, dim description.
		slabels[i] = fmt.Sprintf("%s  %s %s",
			app.bold(padRightDisp(c.label, 22)),
			app.gray("·"),
			app.gray(c.desc))
	}
	spick, err := pickInteractive(app, "How should the next agent receive the context?", slabels)
	if err != nil {
		return "", "", "", err
	}
	return src.ID, target, choices[spick].key, nil
}
