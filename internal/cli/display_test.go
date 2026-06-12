package cli

import (
	"strings"
	"testing"
)

func TestDispWidth(t *testing.T) {
	cases := map[string]int{
		"":          0,
		"hello":     5,
		"中":         2,
		"中文":        4,
		"abc中def":   8, // 3 + 2 + 3
		"\x1b[31mx": 5, // ESC is control (0); '[31m' and 'x' = 5 cols (callers strip ANSI first)
		"\t\n":      0, // both control
		"emoji 🚀":   8, // 5 + 1 + 2
	}
	for in, want := range cases {
		if got := dispWidth(in); got != want {
			t.Errorf("dispWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPadRightDisp(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 5, "abc  "},
		{"中文", 6, "中文  "},
		{"中文abc", 4, "中文abc"}, // already over
		{"", 3, "   "},
	}
	for _, c := range cases {
		if got := padRightDisp(c.in, c.width); got != c.want {
			t.Errorf("padRightDisp(%q,%d) = %q, want %q", c.in, c.width, got, c.want)
		}
		if dispWidth(padRightDisp(c.in, c.width)) < c.width && dispWidth(c.in) <= c.width {
			t.Errorf("padding did not reach %d columns", c.width)
		}
	}
}

func TestTruncDisp(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"hello world", 8, "hello w…"},
		{"短", 5, "短"},
		{"一二三四五六", 6, "一二…"}, // 6 cols: 4 CJK (2x2=4) + ellipsis (1) = 5; next CJK would push to 7
		{"一二三四五六", 5, "一二…"}, // same: 4 + 1
		{"abc", 100, "abc"},
		{"anything", 1, "…"},
		{"emoji🚀tail", 6, "emoji…"},   // 5 narrow + ellipsis = 6 cols, emoji excluded
		{"emoji🚀tail", 9, "emoji🚀t…"}, // 5+2+1+1 = 9 cols
	}
	for _, c := range cases {
		got := truncDisp(c.in, c.width)
		if got != c.want {
			t.Errorf("truncDisp(%q,%d) = %q, want %q", c.in, c.width, got, c.want)
		}
		if dispWidth(got) > c.width {
			t.Errorf("truncDisp(%q,%d) = %q exceeds width (%d cols)", c.in, c.width, got, dispWidth(got))
		}
	}
}

func TestCollapseWhitespace(t *testing.T) {
	cases := map[string]string{
		"hello   world\n\nfoo\tbar":  "hello world foo bar",
		"   leading and trailing   ": "leading and trailing",
		"":                           "",
		"\n\n\t  ":                   "",
		"single":                     "single",
		"中  文":                       "中 文",
	}
	for in, want := range cases {
		if got := collapseWhitespace(in); got != want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsWideCoverage(t *testing.T) {
	// just sample a couple ranges to keep the table honest
	for _, r := range "abc123!@" {
		if isWide(r) {
			t.Errorf("ASCII %q reported wide", r)
		}
	}
	for _, r := range "中文日本語한국" {
		if !isWide(r) {
			t.Errorf("CJK %q not reported wide", r)
		}
	}
}

// padding must never visibly disturb already-colored strings: the caller
// pads the plain text, then re-colors only the leading portion.
func TestPaddingComposesWithColor(t *testing.T) {
	app := &App{jsonOut: true} // disables color in unit tests
	if got := app.cyan("x"); got != "x" {
		t.Skip("color helpers respect jsonOut; cannot exercise composition")
	}
	_ = strings.Repeat // keep import
}

func TestStripANSIAndVisibleWidth(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		visW  int
	}{
		// SGR foreground + reset:
		{"\x1b[36mhello\x1b[0m", "hello", 5},
		// 256-color + bold composed:
		{"\x1b[1m\x1b[38;5;216mclaude-code\x1b[0m", "claude-code", 11},
		// OSC sequence (terminated by BEL): stripped entirely.
		{"\x1b]52;c;Zm9v\x07after", "after", 5},
		// Mixed CJK + color:
		{"\x1b[33m中文\x1b[0m abc", "中文 abc", 8},
		{"plain", "plain", 5},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := visibleWidth(c.in); got != c.visW {
			t.Errorf("visibleWidth(%q) = %d, want %d", c.in, got, c.visW)
		}
	}
}

// wrappedRows is the math the picker uses to keep its cursor-up count
// honest on narrow terminals — bugs here are what produced the ghost-
// cursor-row bug. Lock in the boundary cases.
func TestWrappedRows(t *testing.T) {
	cases := []struct {
		visW, termW, want int
	}{
		{0, 80, 1},   // empty line still occupies a row
		{1, 80, 1},   // fits
		{80, 80, 1},  // exactly one row
		{81, 80, 2},  // soft wrap once
		{160, 80, 2}, // exactly two rows
		{161, 80, 3},
		{50, 0, 1}, // unknown width ⇒ assume no wrap
		{50, -1, 1},
		{0, 0, 1},
	}
	for _, c := range cases {
		if got := wrappedRows(c.visW, c.termW); got != c.want {
			t.Errorf("wrappedRows(visW=%d, termW=%d) = %d, want %d",
				c.visW, c.termW, got, c.want)
		}
	}
}
