// Package redact detects and masks secrets in session content. Backups are
// the moment private sessions leave the machine, so the backup chain runs
// these rules by default; local archive files are never modified.
package redact

import (
	"fmt"
	"regexp"
)

type Rule struct {
	Name string
	Re   *regexp.Regexp
}

// Rules covers high-confidence token shapes. The generic-credential rule is
// last and intentionally broader; matches report the rule name so users can
// judge false positives.
var Rules = []Rule{
	{"aws-access-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"bearer-token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{25,}=*`)},
	{"generic-credential", regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret[_-]?key|access[_-]?token|password)["']?\s*[:=]\s*["'][^"'\s]{12,}["']`)},
}

type Finding struct {
	Rule    string `json:"rule"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line"`
	Preview string `json:"preview"` // masked: first/last 4 chars only
}

// Scan reports secret findings in data without modifying anything.
func Scan(path string, data []byte) []Finding {
	var out []Finding
	for _, rule := range Rules {
		for _, loc := range rule.Re.FindAllIndex(data, -1) {
			out = append(out, Finding{
				Rule:    rule.Name,
				Path:    path,
				Line:    1 + countNewlines(data[:loc[0]]),
				Preview: mask(string(data[loc[0]:loc[1]])),
			})
		}
	}
	return out
}

// Apply returns a copy of data with every match replaced by
// [REDACTED:<rule>]. Originals are left untouched by design.
func Apply(data []byte) []byte {
	for _, rule := range Rules {
		data = rule.Re.ReplaceAll(data, []byte("[REDACTED:"+rule.Name+"]"))
	}
	return data
}

// ApplyKeepLength masks matches in place with 'x', preserving byte offsets.
// Use for binary formats (e.g. SQLite) where changing lengths corrupts the
// file structure.
func ApplyKeepLength(data []byte) []byte {
	out := append([]byte(nil), data...)
	for _, rule := range Rules {
		for _, loc := range rule.Re.FindAllIndex(out, -1) {
			for i := loc[0]; i < loc[1]; i++ {
				out[i] = 'x'
			}
		}
	}
	return out
}

func mask(s string) string {
	if len(s) <= 12 {
		return "****"
	}
	return fmt.Sprintf("%s…%s", s[:4], s[len(s)-4:])
}

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
