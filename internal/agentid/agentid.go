// Package agentid is the single source of truth for "what string
// the user can type to mean Claude Code / Codex / Cursor / …".
//
// The flags ls, search, export, restore, etc. all want the same
// thing: take whatever the user typed (`cc`, `Claude`, `claude_code`)
// and turn it into the canonical name a Source.Name() returns
// (`claude-code`). Centralizing that lets us:
//
//   - tolerate case, separators, and obvious spellings without a wall
//     of if-statements in every command;
//   - give the same "did you mean …" suggestions everywhere when the
//     user mistypes;
//   - keep one list of agents so adding the 13th means touching one
//     table, not seven flag descriptions.
//
// Short codes are deliberately two letters and globally unique across
// the 12 agents — easier to remember than three-letter combos and
// short enough to type without thinking. Where two agents start with
// the same letter the second character disambiguates (cc/cx/cu for
// claude-code/codex/cursor).
package agentid

import (
	"sort"
	"strings"
)

// All canonical agent names, in the order we'd like to print them
// (rough order of "user mindshare" rather than alphabetical — keeps
// help text scannable). Lives here so callers don't have to keep
// inventing slightly-different lists.
var canonicals = []string{
	"claude-code",
	"codex",
	"cursor",
	"opencode",
	"gemini",
	"antigravity",
	"qwen",
	"qoder",
	"iflow",
	"aider",
	"mimocode",
	"kimi-code",
}

// shortCodes maps two-letter shorthand to canonical name. Inverse of
// the table the user sees in `tape ls --help`. We pick characters
// from the actual name (cc / cx / cu / oc / gm / ag / qw / qd / ifl
// / ad / mi / kc) so the mapping is mnemonic, not arbitrary.
var shortCodes = map[string]string{
	"cc": "claude-code",
	"cx": "codex",
	"cu": "cursor",
	"oc": "opencode",
	"gm": "gemini",
	"ag": "antigravity",
	"qw": "qwen",
	"qd": "qoder",
	"if": "iflow",
	"ad": "aider",
	"mi": "mimocode",
	"kc": "kimi-code",
}

// aliases are the obvious longer-form spellings users will reach for
// before discovering the two-letter codes: "claude" for claude-code,
// "mimo" for mimocode, "kimi" for kimi-code, plus the
// no-dash/underscore variants of the canonical names. Anything that
// trips through Normalize's separator-stripping step (claude_code →
// claudecode) is covered by the *stripped* table below — these are
// only for words that don't survive separator removal alone.
var aliases = map[string]string{
	"claude": "claude-code",
	"kimi":   "kimi-code",
	"mimo":   "mimocode",
	// historical naming the upstream projects briefly used / still
	// appear in some docs:
	"openai-codex": "codex",
	"cursor-agent": "cursor",
}

// stripped is the lookup table built from canonicals + aliases with
// all non-alphanumerics removed and case folded. It lets
// "Claude-Code", "claude_code", "claude code", "ClaudeCode" all hit
// the same row without a separate entry per spelling.
var stripped map[string]string

func init() {
	stripped = make(map[string]string, len(canonicals)+len(aliases)+len(shortCodes))
	for _, name := range canonicals {
		stripped[strip(name)] = name
	}
	for alias, canon := range aliases {
		stripped[strip(alias)] = canon
	}
	// short codes go through Normalize too so users can write `--agent CC`
	for short, canon := range shortCodes {
		stripped[short] = canon
	}
}

// Normalize takes whatever the user typed and returns the canonical
// agent name plus an ok bool. An empty input is treated as "no
// filter set" (ok=true, returns ""), since every caller already
// short-circuits on the empty case and forcing them to special-case
// it again is noisy.
//
// The matcher is forgiving: case-insensitive, separator-insensitive
// (claude-code / claude_code / claude.code / Claude Code all hit),
// and recognizes the two-letter codes plus a few obvious aliases.
// Anything unrecognized returns ok=false; callers should pair the
// failure with Suggest() to produce a usable error message.
func Normalize(input string) (string, bool) {
	if input == "" {
		return "", true
	}
	if canon, ok := stripped[strip(input)]; ok {
		return canon, true
	}
	return "", false
}

// Canonicals returns the list of agent names a Source.Name() can
// return. Returned slice is a copy — callers can mutate it freely.
func Canonicals() []string {
	out := make([]string, len(canonicals))
	copy(out, canonicals)
	return out
}

// Short returns the two-letter shorthand for a canonical name, or
// "" if the input isn't a known canonical. Useful for printing
// "(cc)" hints next to agent names in help text.
func Short(canonical string) string {
	for short, canon := range shortCodes {
		if canon == canonical {
			return short
		}
	}
	return ""
}

// Suggest returns up to limit canonical names ranked by similarity
// to the user's (presumably mistyped) input. The ranker is a cheap
// shared-character / prefix score — good enough for "did you mean"
// hints, not good enough to actually depend on for correctness.
func Suggest(input string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	q := strip(input)
	type scored struct {
		name  string
		score int
	}
	scores := make([]scored, 0, len(canonicals))
	for _, name := range canonicals {
		scores = append(scores, scored{name: name, score: similarity(q, strip(name))})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})
	out := make([]string, 0, limit)
	for _, s := range scores {
		if s.score <= 0 {
			break
		}
		out = append(out, s.name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// HelpLine renders the canonical names with their short codes, the
// way help text wants them: "claude-code (cc), codex (cx), …". One
// string so call sites can drop it straight into a flag description
// without rebuilding the format every time.
func HelpLine() string {
	parts := make([]string, len(canonicals))
	for i, name := range canonicals {
		if short := Short(name); short != "" {
			parts[i] = name + " (" + short + ")"
		} else {
			parts[i] = name
		}
	}
	return strings.Join(parts, ", ")
}

// strip lowercases and drops every non-alphanumeric rune. Cheap and
// allocation-light; called once per lookup, so the linear scan is
// fine for inputs that are always agent-name-sized.
func strip(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// similarity scores two stripped strings. We don't pull in
// levenshtein because the inputs are tiny and a shared-char count +
// prefix bonus separates the right suggestions from the wrong ones
// well enough in practice (`cluade` → claude-code, `codx` → codex,
// `claud` → claude-code, etc.).
func similarity(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1000
	}
	score := 0
	// Prefix bonus dominates: typos are usually at the end.
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] == b[i] {
			score += 10
		} else {
			break
		}
	}
	// Shared characters anywhere: cheap multi-set intersection.
	set := make(map[byte]int, len(b))
	for i := 0; i < len(b); i++ {
		set[b[i]]++
	}
	for i := 0; i < len(a); i++ {
		if set[a[i]] > 0 {
			set[a[i]]--
			score++
		}
	}
	return score
}
