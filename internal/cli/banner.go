package cli

import (
	"strings"
)

// banner is the splash shown at the top of `tape --help`. The wordmark
// uses ANSI Shadow glyphs colored in each agent's brand color so the four
// letters of TAPE literally are Tape · Anthropic · Codex · ...everyone's
// session in one place. The 'E' stays neutral cream.
//
// xterm-256 indices, intentionally on the light side so the banner glows
// against dark terminals without screaming:
//
//	T → 216 (#ffaf87 washed Anthropic orange)
//	A → 189 (#d7d7ff pale Codex periwinkle)
//	P → 252 (#d0d0d0 Cursor silver)
//	E → 230 (#ffffd7 tape cream)
//
// Drops back to gray when colors are off.
func banner(app *App, version string) string {
	letters := [][]string{
		{ // T
			"████████╗",
			"╚══██╔══╝",
			"   ██║   ",
			"   ██║   ",
			"   ██║   ",
			"   ╚═╝   ",
		},
		{ // A
			" █████╗ ",
			"██╔══██╗",
			"███████║",
			"██╔══██║",
			"██║  ██║",
			"╚═╝  ╚═╝",
		},
		{ // P
			"██████╗ ",
			"██╔══██╗",
			"██████╔╝",
			"██╔═══╝ ",
			"██║     ",
			"╚═╝     ",
		},
		{ // E
			"███████╗",
			"██╔════╝",
			"█████╗  ",
			"██╔══╝  ",
			"███████╗",
			"╚══════╝",
		},
	}
	// Brand-aligned 256-color palette, one per letter:
	//   T → 216  light peach      (Anthropic, washed)
	//   A → 189  pale periwinkle  (Codex)
	//   P → 252  silver           (Cursor)
	//   E → 230  cream            (Tape)
	colors := []int{colorClaude, colorCodex, colorCursor, 230}

	var b strings.Builder
	b.WriteByte('\n') // breathing room above the wordmark
	for row := 0; row < 6; row++ {
		b.WriteString("  ")
		for i, letter := range letters {
			b.WriteString(app.brand(colors[i], letter[row]))
			b.WriteByte(' ')
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	tagline := app.bold("Record, search and replay your AI coding sessions.")
	subtag := app.dim("Nothing gets lost on tape.")
	if version == "" {
		version = "dev"
	}
	meta := app.gray("v"+version+"  ·  ") +
		app.cyan("github.com/chenhg5/tape")

	b.WriteString("  " + tagline + "\n")
	b.WriteString("  " + subtag + "\n\n")
	b.WriteString("  " + meta + "\n")
	return b.String()
}
