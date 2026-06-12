package cli

import (
	"strings"
)

// banner is the splash shown at the top of `tape --help`. The wordmark
// uses ANSI Shadow glyphs colored in each agent's brand color so the four
// letters of TAPE literally are Tape · Anthropic · Codex · ...everyone's
// session in one place. The 'E' stays neutral cream.
//
// xterm-256 indices (matched to official hex):
//
//	T → 173 (#d7875f ≈ #d97757 Anthropic Crail)
//	A → 36  (#00af87 ≈ #10a37f OpenAI green)
//	P → 202 (#ff5f00 ≈ #f54e00 Cursor ember)
//	E → 230 (cream)
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
			"██║     ",
			"╚══════╝",
		},
	}
	colors := []int{173, 36, 202, 230}

	var b strings.Builder
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
