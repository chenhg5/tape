package model

import "strings"

// ProjectSlug normalizes a working directory into a stable project key,
// e.g. "/root/code/spaceship/tape" -> "root-code-spaceship-tape".
// It is the grouping key used across agents for the same project.
func ProjectSlug(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	s := strings.ReplaceAll(cwd, "\\", "/")
	s = strings.Trim(s, "/")
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == ' ' || r == ':':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
