// Package scan holds small helpers shared by source parsers.
package scan

import "strings"

// MaxToolIO caps tool input/output text kept in the normalized IR.
// Full content always survives in the raw archive copy.
const MaxToolIO = 4096

func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 { // don't split a UTF-8 rune
		cut--
	}
	return s[:cut] + "…"
}

// FirstLine returns the first non-empty line, truncated to max bytes.
func FirstLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			return Truncate(l, max)
		}
	}
	return ""
}
