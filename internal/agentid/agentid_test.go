package agentid

import (
	"sort"
	"strings"
	"testing"
)

// TestNormalizeCanonicalsRoundTrip pins the single most important
// invariant: every canonical name normalizes back to itself, no
// matter the case or separator. If we ever change the canonical
// list, this test forces us to keep the round-trip working.
func TestNormalizeCanonicalsRoundTrip(t *testing.T) {
	for _, name := range Canonicals() {
		got, ok := Normalize(name)
		if !ok || got != name {
			t.Errorf("Normalize(%q) = (%q, %v); want (%q, true)", name, got, ok, name)
		}
		// Upper-cased and de-dashed variants must also resolve.
		variants := []string{strings.ToUpper(name), strings.ReplaceAll(name, "-", "_"), strings.ReplaceAll(name, "-", "")}
		for _, v := range variants {
			if got, ok := Normalize(v); !ok || got != name {
				t.Errorf("Normalize(%q) = (%q, %v); want (%q, true)", v, got, ok, name)
			}
		}
	}
}

// TestNormalizeShortCodes: every two-letter code maps to exactly
// one agent, and the codes are globally unique (no two canonicals
// share a code).
func TestNormalizeShortCodes(t *testing.T) {
	cases := map[string]string{
		"cc": "claude-code", "cx": "codex", "cu": "cursor", "oc": "opencode",
		"gm": "gemini", "ag": "antigravity", "qw": "qwen", "qd": "qoder",
		"if": "iflow", "ad": "aider", "mi": "mimocode", "kc": "kimi-code",
	}
	for short, want := range cases {
		if got, ok := Normalize(short); !ok || got != want {
			t.Errorf("Normalize(%q) = (%q, %v); want (%q, true)", short, got, ok, want)
		}
		// upper-case still works
		if got, _ := Normalize(strings.ToUpper(short)); got != want {
			t.Errorf("Normalize(%q upper) = %q; want %q", short, got, want)
		}
	}

	// uniqueness: each canonical appears at most once across short
	// codes, and every canonical has a code (so help text never
	// shows a name without one).
	seen := map[string]int{}
	for _, canon := range cases {
		seen[canon]++
	}
	for _, canon := range Canonicals() {
		if seen[canon] != 1 {
			t.Errorf("canonical %q has %d short codes; want exactly 1", canon, seen[canon])
		}
	}
}

// TestNormalizeAliases covers the longer-form aliases that aren't
// just separator-stripped versions of canonicals (`claude` for
// claude-code etc.).
func TestNormalizeAliases(t *testing.T) {
	for input, want := range map[string]string{
		"claude":       "claude-code",
		"Claude":       "claude-code",
		"kimi":         "kimi-code",
		"mimo":         "mimocode",
		"openai-codex": "codex",
		"cursor-agent": "cursor",
		"claude code":  "claude-code",
		"claude.code":  "claude-code",
		"ClaudeCode":   "claude-code",
		"kimi_code":    "kimi-code",
	} {
		if got, ok := Normalize(input); !ok || got != want {
			t.Errorf("Normalize(%q) = (%q, %v); want (%q, true)", input, got, ok, want)
		}
	}
}

// TestNormalizeEmptyAndUnknown documents the two non-happy paths:
// empty input is "no filter" (ok=true, "") so callers can pass
// through; unknown input is the failure case for Suggest() to
// rescue.
func TestNormalizeEmpty(t *testing.T) {
	if got, ok := Normalize(""); !ok || got != "" {
		t.Errorf("Normalize(\"\") = (%q, %v); want (\"\", true)", got, ok)
	}
}

func TestNormalizeUnknown(t *testing.T) {
	for _, bad := range []string{"clauude", "nope", "xz", "????"} {
		if got, ok := Normalize(bad); ok {
			t.Errorf("Normalize(%q) = (%q, true); want ok=false", bad, got)
		}
	}
}

// TestSuggestRankingPicksObviousTypos checks that the ranker — even
// though it's a cheap one — recovers the obvious one-letter typos
// users actually type. The exact ordering past position 1 isn't a
// contract; we only assert the top hit.
func TestSuggestRankingPicksObviousTypos(t *testing.T) {
	cases := map[string]string{
		"cluade":     "claude-code",
		"claud":      "claude-code",
		"codx":       "codex",
		"opencod":    "opencode",
		"kimicod":    "kimi-code",
		"antigravit": "antigravity",
	}
	for input, want := range cases {
		got := Suggest(input, 3)
		if len(got) == 0 || got[0] != want {
			t.Errorf("Suggest(%q) top = %v; want %q", input, got, want)
		}
	}
}

// TestSuggestLimit honors the limit argument and never returns
// canonicals scored at zero (e.g. completely unrelated junk).
func TestSuggestLimit(t *testing.T) {
	if got := Suggest("claude", 2); len(got) > 2 {
		t.Errorf("Suggest(_, 2) = %v; want ≤2", got)
	}
	// pure punctuation has nothing in common with any name → empty
	if got := Suggest("____", 5); len(got) != 0 {
		t.Errorf("Suggest(\"____\") = %v; want []", got)
	}
}

// TestHelpLineContainsEveryAgentAndCode ensures the rendered help
// string really does list every agent with its short code — that
// way `tape ls --help` cannot silently lose an entry when we add a
// new agent.
func TestHelpLineContainsEveryAgentAndCode(t *testing.T) {
	line := HelpLine()
	for _, canon := range Canonicals() {
		if !strings.Contains(line, canon) {
			t.Errorf("HelpLine missing canonical %q: %s", canon, line)
		}
		short := Short(canon)
		if short == "" {
			t.Errorf("Short(%q) is empty", canon)
		}
		if !strings.Contains(line, "("+short+")") {
			t.Errorf("HelpLine missing (%s) for %q: %s", short, canon, line)
		}
	}
}

// TestCanonicalsListMatchesSourcePackages guards against the agentid
// table drifting out of sync with the source.Name() implementations.
// We can't import the source packages from here (that would invert
// dependency direction), so we just pin the list size and contents
// against what the rest of the codebase expects.
func TestCanonicalsListIsStable(t *testing.T) {
	want := []string{
		"aider", "antigravity", "claude-code", "codex", "cursor",
		"gemini", "iflow", "kimi-code", "mimocode", "opencode",
		"qoder", "qwen",
	}
	got := Canonicals()
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Canonicals() = %v; want %v", got, want)
	}
}
