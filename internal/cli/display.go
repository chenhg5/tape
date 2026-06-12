package cli

import (
	"regexp"
	"strings"
	"unicode"
)

// ansiSeqRe matches the ANSI control sequences we actually emit:
// SGR / cursor moves (`\x1b[…<letter>`) and OSC strings closed by BEL
// or ST (`\x1b]…\x07` / `\x1b]…\x1b\\`). Good enough for stripping the
// styled prefixes we ourselves produce; not a general ANSI parser.
var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// stripANSI returns s with our own ANSI escape sequences removed. Use
// it before any width / column calculation on already-colored strings.
func stripANSI(s string) string { return ansiSeqRe.ReplaceAllString(s, "") }

// visibleWidth is dispWidth that also ignores ANSI escapes — the right
// answer for picker rows and other layout code that consumes
// already-colored input.
func visibleWidth(s string) int { return dispWidth(stripANSI(s)) }

// wrappedRows estimates how many terminal rows a single logical line
// of `visW` visible columns occupies when the terminal is `termW`
// columns wide. Used by the picker to keep its cursor-up math honest
// when items are too long to fit on one row. `termW <= 0` means
// "unknown size, assume no wrapping" — better to clear too few rows
// than to mis-clear what we can't see.
func wrappedRows(visW, termW int) int {
	if termW <= 0 || visW <= 0 {
		return 1
	}
	return (visW + termW - 1) / termW
}

// dispWidth approximates how many terminal columns a string occupies:
// East-Asian wide characters and common emoji ranges count as 2, ASCII
// and other narrow runes count as 1, control characters count as 0.
// We do not parse ANSI escape codes — callers must pass plain text.
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// control: zero
		case isWide(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

// isWide reports whether r is rendered as two columns in most terminals.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e, // CJK radicals, punctuation
		r >= 0x3041 && r <= 0x33ff, // Hiragana, Katakana, Bopomofo, ...
		r >= 0x3400 && r <= 0x4dbf, // CJK extension A
		r >= 0x4e00 && r <= 0x9fff, // CJK unified ideographs
		r >= 0xa000 && r <= 0xa4cf, // Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe4f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // Fullwidth
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, // Emoji
		r >= 0x1f680 && r <= 0x1f6ff,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x2fffd: // CJK extensions B-F
		return true
	}
	return false
}

// padRightDisp pads s with spaces to width display columns.
func padRightDisp(s string, width int) string {
	w := dispWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncDisp truncates s to fit within width display columns, appending "…"
// (1 column) when truncation happens. Multi-byte/wide runes are never
// split. width must be >= 1.
func truncDisp(s string, width int) string {
	if dispWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	budget := width - 1 // reserve a column for the ellipsis
	used := 0
	var b strings.Builder
	for _, r := range s {
		rw := 1
		if isWide(r) {
			rw = 2
		}
		if used+rw > budget {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteRune('…')
	return b.String()
}

// collapseWhitespace turns runs of \r\n\t and spaces into a single space, so
// snippet rendering does not break the column layout. Surrounding spaces
// are stripped.
func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}
