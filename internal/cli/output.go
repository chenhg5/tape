package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/term"
)

// Output policy (agent-cli-guide principles 3 & 4):
//   - stdout carries data, stderr carries messages
//   - JSON when --json is set OR stdout is not a TTY (agents pipe)
//   - colors only on a TTY and when NO_COLOR/TERM=dumb are absent

func stdoutIsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
func stdinIsTTY() bool  { return term.IsTerminal(int(os.Stdin.Fd())) }

func (a *App) useJSON() bool { return a.jsonOut || !stdoutIsTTY() }

// interactive reports whether we can safely ask the user a question:
// both stdin and stdout must be a TTY (so they see prompts AND can type),
// --json must be off, and NO_COLOR/TERM=dumb don't matter — prompts work
// in plain ASCII too. Agents and piped runs always answer "no", which
// keeps their error path strict and scriptable.
func (a *App) interactive() bool {
	return !a.jsonOut && stdoutIsTTY() && stdinIsTTY()
}

func (a *App) useColor() bool {
	if !stdoutIsTTY() {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return os.Getenv("TERM") != "dumb"
}

func (a *App) paint(code, s string) string {
	if !a.useColor() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (a *App) bold(s string) string    { return a.paint("1", s) }
func (a *App) dim(s string) string     { return a.paint("2", s) }
func (a *App) cyan(s string) string    { return a.paint("36", s) }
func (a *App) green(s string) string   { return a.paint("32", s) }
func (a *App) yellow(s string) string  { return a.paint("33", s) }
func (a *App) magenta(s string) string { return a.paint("35", s) }
func (a *App) red(s string) string     { return a.paint("31", s) }
func (a *App) gray(s string) string    { return a.paint("90", s) }

// brand wraps s in a 256-color SGR escape; falls back to plain text when
// colors are off (NO_COLOR, non-TTY, etc.).
func (a *App) brand(xterm256 int, s string) string {
	return a.paint(fmt.Sprintf("38;5;%d", xterm256), s)
}

// Agent brand palette. All entries live in the upper xterm-256 cube
// (#afxxxx / #d7xxxx / #ffxxxx), giving us washed-out pastels that read
// cleanly on both dark and light terminals without the eye-searing
// saturation of the original brand colors. We aim for one row in each
// hue family — blue, pink, green, peach — so 10 agents stay visually
// distinct even at a glance:
//
//	cursor      → xterm 252  silver-gray      (≈ #d0d0d0, Cursor monochrome IDE)
//	gemini      → xterm 153  pale-sky         (≈ #afd7ff, washed Google blue)
//	antigravity → xterm 147  soft-periwinkle  (≈ #afafff, gemini-successor violet)
//	codex       → xterm 189  pale-periwinkle  (≈ #d7d7ff, faded dreamy violet)
//	qwen        → xterm 218  cherry-blossom   (≈ #ffafd7, washed Alibaba pink)
//	qoder       → xterm 225  light-lilac      (≈ #ffd7ff, iflow-successor pastel)
//	claude-code → xterm 216  light-peach      (≈ #ffaf87, washed Anthropic orange)
//	mimocode    → xterm 217  soft-coral       (≈ #ffafaf, washed Xiaomi orange)
//	kimi-code   → xterm 183  plum-mist        (≈ #d7afff, Moonshot lunar violet)
//	aider       → xterm 223  soft-cream       (≈ #ffd7af, paper / pencil pastel)
//	iflow       → xterm 158  mint             (≈ #afffd7, washed iFlow teal)
//	opencode    → xterm 194  pale-leaf        (≈ #d7ffd7, washed terminal green)
const (
	colorClaude      = 216
	colorCodex       = 189
	colorCursor      = 252
	colorGemini      = 153
	colorAntigravity = 147
	colorQwen        = 218
	colorQoder       = 225
	colorIFlow       = 158
	colorAider       = 223
	colorOpencode    = 194
	colorMimocode    = 217
	colorKimiCode    = 183
)

// agentColor paints the agent name in its brand color so rows group visually
// without dominating the line.
func (a *App) agentColor(agent string) string {
	switch agent {
	case "claude-code":
		return a.brand(colorClaude, agent)
	case "codex":
		return a.brand(colorCodex, agent)
	case "cursor":
		return a.brand(colorCursor, agent)
	case "gemini":
		return a.brand(colorGemini, agent)
	case "qwen":
		return a.brand(colorQwen, agent)
	case "iflow":
		return a.brand(colorIFlow, agent)
	case "aider":
		return a.brand(colorAider, agent)
	case "opencode":
		return a.brand(colorOpencode, agent)
	case "antigravity":
		return a.brand(colorAntigravity, agent)
	case "qoder":
		return a.brand(colorQoder, agent)
	case "mimocode":
		return a.brand(colorMimocode, agent)
	case "kimi-code":
		return a.brand(colorKimiCode, agent)
	default:
		return a.yellow(agent)
	}
}

// lead prints one blank line at the top of a human-facing output block.
// Every renderXxx helper (and the human branch of any command that
// writes a final summary) calls this once on entry so the first row of
// real content never sits flush against the user's previous prompt —
// which felt cramped, especially in zsh themes that don't add their own
// trailing newline. The JSON path skips this because agents parse line
// by line and a leading blank line breaks naive JSONL consumers.
func (a *App) lead() {
	if a.useJSON() {
		return
	}
	fmt.Println()
}

// cliError is a machine-actionable error (agent-cli-guide principle 9).
type cliError struct {
	Type       string `json:"error"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func (e cliError) Error() string {
	if e.Suggestion != "" {
		return e.Message + " (try: " + e.Suggestion + ")"
	}
	return e.Message
}

// Is lets errors.Is(cliError{Type:"no_results"}, ErrNoResults) succeed,
// so commands can return a Suggestion-carrying cliError on the empty
// path AND keep the semantic exit code (3) that drives agent branching.
// Without this, wrapping no-results in cliError would silently demote
// the exit to 1 — every CI script that branches on "code 3 == nothing
// found" would break.
func (e cliError) Is(target error) bool {
	return target == ErrNoResults && e.Type == "no_results"
}

// reportError writes the error to stderr, as JSON when in robot mode.
// In human mode a cliError's Suggestion is broken onto its own line with
// a "try:" prefix so multi-sentence hints stay scannable.
func (a *App) reportError(err error) {
	if a.useJSON() {
		var ce cliError
		switch v := err.(type) {
		case cliError:
			ce = v
		default:
			ce = cliError{Type: "error", Message: err.Error()}
		}
		json.NewEncoder(os.Stderr).Encode(ce)
		return
	}
	if ce, ok := err.(cliError); ok && ce.Suggestion != "" {
		fmt.Fprintf(os.Stderr, "%s %s\n", a.red("tape:"), ce.Message)
		fmt.Fprintf(os.Stderr, "  %s %s\n", a.gray("try:"), ce.Suggestion)
		return
	}
	fmt.Fprintln(os.Stderr, "tape:", err)
}
