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
	drawn := 0 // total visual rows the previous frame occupied minus 1
	hint := app.gray("[↑/↓ move · Enter select · 1-9 jump · q quit]")

	// Detect terminal width once per render so we can account for soft
	// wraps: a single logical row of a long item can wrap into 2-3 real
	// rows on a narrow window, and an erase that only walks back
	// len(items) rows leaves the wrap leftovers on screen as ghost
	// copies of the cursor row. termW <= 0 ⇒ size unknown ⇒ wrappedRows
	// returns 1 and we degrade to the pre-fix behavior.
	termW, _, _ := term.GetSize(fd)

	render := func() {
		// Erase previous frame: jump to the first row of the previous
		// render and clear from there to end-of-screen. `drawn` is
		// kept in real (post-wrap) rows so this still works on narrow
		// terminals.
		if drawn > 0 {
			fmt.Fprintf(out, "\x1b[%dA", drawn)
			fmt.Fprint(out, "\x1b[J")
		}
		var b strings.Builder
		// Cursor anchor logic: after the loop + hint emit, the cursor
		// sits on the hint's final wrapped row at column 0. The first
		// item row is `totalRows - 1` rows above (we don't subtract
		// the hint row separately — hint's own wrapped rows are
		// counted in totalRows). On the first paint drawn stays 0
		// because there's no previous frame to erase.
		totalRows := 0
		for i, it := range items {
			if i == cursor {
				b.WriteString(app.cyan("▸ "))
				b.WriteString(app.bold(it))
			} else {
				b.WriteString("  ")
				b.WriteString(it)
			}
			b.WriteString("\r\n")
			// 2 leading columns for the "▸ " / "  " prefix.
			totalRows += wrappedRows(visibleWidth(it)+2, termW)
		}
		b.WriteString(hint)
		b.WriteString("\r")
		totalRows += wrappedRows(visibleWidth(hint), termW)
		fmt.Fprint(out, b.String())
		drawn = totalRows - 1
		if drawn < 0 {
			drawn = 0
		}
	}
	finish := func() {
		// Clear the hint line (may have wrapped) and emit a real
		// newline so subsequent output (another picker, a success
		// message) starts at column 0 on its own row. The menu itself
		// is left rendered above — it doubles as an audit trail of
		// what the user picked. `[J` clears from the cursor (col 0 of
		// the hint's last wrapped row) to end-of-screen, taking the
		// rest of the wrapped hint with it.
		fmt.Fprint(out, "\x1b[J\r\n")
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
