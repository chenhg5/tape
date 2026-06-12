package cli

// Why we don't pull in a TUI library:
//
//   - The needs here are tiny: a single-column list, ↑/↓ + Enter + quit.
//     bubbletea/promptui/survey would each add 5–20 transitive deps for
//     something that's ~80 lines of escape sequences.
//   - tape's whole pitch is "small Go binary, no Node, no Python". A
//     dependency creep tax on every menu makes that pitch weaker.
//   - x/term is already a dependency (used for IsTerminal); MakeRaw and
//     restore handle the messy parts.
//
// Fallback policy:
//
//   - Not a TTY, dumb terminal, or MakeRaw failed (some sandboxes,
//     Windows without ANSI, Cygwin pipes) → degrade to the numbered
//     prompt from prompt.go. Same return type, same abort semantics.

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// pickInteractive shows an arrow-key driven list and returns the chosen
// index. items must be pre-formatted (incl. coloring) by the caller —
// the picker only handles ▸ cursor + dimming so it stays composable.
//
// Keys: ↑/k previous, ↓/j next, Enter pick, 1-9 jump+pick, q/ESC/Ctrl-C
// abort. On non-interactive stdin we fall back to numeric input so
// scripts feeding stdin still work.
func pickInteractive(app *App, title string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, fmt.Errorf("%s: nothing to choose from", title)
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || os.Getenv("TERM") == "dumb" {
		return promptChoice(app, title, items)
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return promptChoice(app, title, items)
	}
	defer term.Restore(fd, old)

	out := os.Stderr
	// Hide the cursor while we own the screen real estate. Always
	// restored, even on panic, via the deferred Fprint.
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h")

	fmt.Fprintf(out, "\r\n%s\r\n", app.bold(title))

	cursor := 0
	drawn := 0
	hint := app.gray("[↑/↓ move · Enter select · 1-9 jump · q quit]")

	render := func() {
		// Erase previous frame: jump to the line we first wrote and
		// clear to end-of-screen. Cheap and works on every ANSI term.
		if drawn > 0 {
			fmt.Fprintf(out, "\x1b[%dA", drawn)
			fmt.Fprint(out, "\x1b[J")
		}
		var b strings.Builder
		for i, it := range items {
			if i == cursor {
				b.WriteString(app.cyan("▸ "))
				b.WriteString(app.bold(it))
			} else {
				b.WriteString("  ")
				b.WriteString(it)
			}
			b.WriteString("\r\n")
		}
		b.WriteString(hint)
		b.WriteString("\r")
		fmt.Fprint(out, b.String())
		// Cursor anchor: the hint trailing "\r" leaves the cursor on
		// the hint row, column 0. The first item row is exactly
		// len(items) rows above. We don't add +1 for hint because
		// \x1b[J below clears from the anchor to the end of screen,
		// taking the hint row with it on its way down. Counting +1
		// would land us on the title row and wipe it on every keypress.
		drawn = len(items)
	}
	finish := func() {
		// Clear the hint line and emit a real newline so subsequent
		// output (another picker, a success message) starts at column
		// 0 on its own row. The menu itself is left rendered above —
		// it doubles as an audit trail of what the user picked.
		fmt.Fprint(out, "\x1b[2K\r\n")
	}

	render()

	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			finish()
			return 0, errPromptAborted
		}
		b := buf[:n]

		// Arrow keys arrive as ESC [ A / B in one read; treat them first.
		if n >= 3 && b[0] == 0x1b && b[1] == '[' {
			switch b[2] {
			case 'A': // up
				if cursor > 0 {
					cursor--
					render()
				}
			case 'B': // down
				if cursor < len(items)-1 {
					cursor++
					render()
				}
			}
			continue
		}

		switch b[0] {
		case '\r', '\n':
			finish()
			return cursor, nil
		case 'q', 'Q', 0x03 /* Ctrl-C */, 0x04 /* Ctrl-D */, 0x1b /* lone ESC */ :
			finish()
			return 0, errPromptAborted
		case 'k', 'K':
			if cursor > 0 {
				cursor--
				render()
			}
		case 'j', 'J':
			if cursor < len(items)-1 {
				cursor++
				render()
			}
		default:
			// Number shortcut: jump and confirm in one keystroke,
			// matching what the numeric fallback would do.
			if b[0] >= '1' && b[0] <= '9' {
				idx := int(b[0] - '1')
				if idx < len(items) {
					cursor = idx
					render()
					finish()
					return cursor, nil
				}
			}
		}
	}
}
