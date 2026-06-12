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

func (a *App) useJSON() bool { return a.jsonOut || !stdoutIsTTY() }

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

// agentColor paints the agent name in a color that evokes that agent's
// own visual identity, so rows group visually:
//
//	claude-code → xterm 173  warm crail orange     (Anthropic)
//	codex       → xterm 105  dreamy periwinkle     (≈ #808fef)
//	cursor      → xterm 250  geek gray             (Cursor's monochrome IDE feel)
func (a *App) agentColor(agent string) string {
	switch agent {
	case "claude-code":
		return a.brand(173, agent)
	case "codex":
		return a.brand(105, agent)
	case "cursor":
		return a.brand(250, agent)
	default:
		return a.yellow(agent)
	}
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

// reportError writes the error to stderr, as JSON when in robot mode.
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
	fmt.Fprintln(os.Stderr, "tape:", err)
}
