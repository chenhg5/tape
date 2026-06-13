package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestParseSince(t *testing.T) {
	if ts, err := parseSince(""); err != nil || !ts.IsZero() {
		t.Errorf("empty: %v %v", ts, err)
	}
	if ts, err := parseSince("2026-01-31"); err != nil || ts.Format("2006-01-02") != "2026-01-31" {
		t.Errorf("date: %v %v", ts, err)
	}
	for in, wantAgo := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		ts, err := parseSince(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got := time.Since(ts)
		if got < wantAgo-time.Minute || got > wantAgo+time.Minute {
			t.Errorf("%s: off by %v", in, got-wantAgo)
		}
	}
	if _, err := parseSince("eleventy"); err == nil {
		t.Error("garbage must error")
	}
	if _, err := parseSince("eleventy"); !isUsageError(err) {
		t.Error("garbage --since must be a usage error (exit 2)")
	}
}

func TestShortID(t *testing.T) {
	cases := map[string]string{
		// UUID-style IDs: head-6 + … + tail-4 keeps both halves.
		"codex/019ea0af-3d6a-7393-8ccf-a4ae49f116c3": "codex/019ea0…16c3",
		"claude-code/7dd2afaf-1234-5678":             "claude-code/7dd2af…5678",
		// opencode-family IDs share a fixed `ses_141bXXX` prefix; the
		// tail-4 is what actually distinguishes one session from another.
		// Regression for tape#mimo-short-id: prior shortID truncated to
		// `mimocode/ses_141b` and made every session look identical.
		"mimocode/ses_141b69756ffeXmTRdFwDAW8QIU": "mimocode/ses_14…8QIU",
		"opencode/ses_141dcf748ffeE800iK5eLs7MzE": "opencode/ses_14…7MzE",
		// Pathologically short IDs and unprefixed IDs are kept verbatim.
		"short":            "short",
		"under12char":      "under12char",
		"longerthantwelve": "longer…elve",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShortIDDistinguishesSimilarPrefixes pins the actual bug: three
// distinct mimocode sessions whose IDs share the first 8 chars must
// produce three distinct short labels.
func TestShortIDDistinguishesSimilarPrefixes(t *testing.T) {
	ids := []string{
		"mimocode/ses_141b69756ffeXmTRdFwDAW8QIU",
		"mimocode/ses_141b69751ffes63wTfiifT7N3V",
		"mimocode/ses_141b69725ffeSvkt6QI5HLic13",
	}
	seen := map[string]string{}
	for _, id := range ids {
		s := shortID(id)
		if prev, ok := seen[s]; ok {
			t.Errorf("shortID collision: %q and %q both → %q", prev, id, s)
		}
		seen[s] = id
	}
}

// TestShellQuote pins the POSIX single-quote escape rule we rely on
// when building remote ssh commands (a stray quote inside cwd must not
// break the shell). The escape sequence '\'' is awkward but standard;
// every POSIX shell handles it.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":                                          "''",
		"/root/code/spaceship":                      "'/root/code/spaceship'",
		"/home/me/with space":                       "'/home/me/with space'",
		"/tmp/it's/quoted":                          `'/tmp/it'\''s/quoted'`,
		`/path/with "double" and 'single' quotes`:   `'/path/with "double" and '\''single'\'' quotes'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateHelper(t *testing.T) {
	if got := truncate("中文标题超长需要截断", 6); !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
	if got := truncate("ok", 10); got != "ok" {
		t.Errorf("got %q", got)
	}
}

func TestStartHint(t *testing.T) {
	cases := map[string]string{
		"claude-code": "claude",
		"codex":       "codex",
		"cursor":      "cursor-agent",
		"gemini":      "gemini",
		"qwen":        "qwen",
		"iflow":       "iflow",
		"aider":       "aider",
		"opencode":    "opencode",
		"antigravity": "agy",
		"qoder":       "qodercli",
		"mimocode":    "mimo",
		"kimi-code":   "kimi",
	}
	for agent, bin := range cases {
		if hint := startHint(agent, "h.md"); !strings.HasPrefix(hint, bin+" ") || !strings.Contains(hint, "h.md") {
			t.Errorf("%s hint = %q", agent, hint)
		}
	}
}

func TestCLIErrorFormatting(t *testing.T) {
	e := cliError{Type: "not_found", Message: "no such session", Suggestion: "tape ls"}
	if !strings.Contains(e.Error(), "tape ls") {
		t.Errorf("suggestion missing from %q", e.Error())
	}
	bare := cliError{Type: "x", Message: "boom"}
	if bare.Error() != "boom" {
		t.Errorf("got %q", bare.Error())
	}
}

func TestSchemaDescribe(t *testing.T) {
	root := &cobra.Command{Use: "tape", Short: "root"}
	exp := &cobra.Command{Use: "export", Short: "export the archive", Example: "  tape export"}
	exp.Flags().Bool("dry-run", false, "preview")
	exp.Flags().String("output", "", "output file path")
	root.AddCommand(exp, &cobra.Command{Use: "help"})

	s := describe(root)
	if len(s.Subcommands) != 1 || s.Subcommands[0].Name != "export" {
		t.Fatalf("help command must be hidden, got %+v", s.Subcommands)
	}
	p := s.Subcommands[0]
	if p.Name != "export" || p.Example == "" {
		t.Errorf("export schema: %+v", p)
	}
	names := map[string]string{}
	for _, f := range p.Flags {
		names[f.Name] = f.Type
	}
	if names["--dry-run"] != "bool" || names["--output"] != "string" {
		t.Errorf("flags: %v", names)
	}
}

// TestResolveAgentFilter exercises the wrapper that every command
// using --agent / --to funnels through. Canonical/shortcode/alias
// each resolve, the empty string passes through (= no filter), and
// an unknown name surfaces a usage error with "did you mean…".
func TestResolveAgentFilter(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"claude-code": "claude-code",
		"cc":          "claude-code",
		"Claude":      "claude-code",
		"oc":          "opencode",
		"OpenCode":    "opencode",
		"kimi":        "kimi-code",
	}
	for in, want := range cases {
		got, err := resolveAgentFilter(in)
		if err != nil {
			t.Errorf("resolveAgentFilter(%q) err: %v", in, err)
		}
		if got != want {
			t.Errorf("resolveAgentFilter(%q) = %q, want %q", in, got, want)
		}
	}

	_, err := resolveAgentFilter("clauude")
	if err == nil {
		t.Fatal("expected error for typo")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("typo error missing suggestion: %v", err)
	}
}

// TestRedactArtifactKeepLengthForDB pins the per-extension policy
// the export writer uses: .db files get masked in place (so the
// SQLite header / page boundaries stay valid), everything else gets
// the readable [REDACTED:<rule>] marker. Both happen during the
// streaming write, never against the local file.
func TestRedactArtifactKeepLengthForDB(t *testing.T) {
	plain := []byte("hello AKIAIOSFODNN7EXAMPLE world")
	if out := redactArtifact("codex/p/s/session.json", plain); !strings.Contains(string(out), "[REDACTED:") {
		t.Errorf("text path lost the readable marker: %q", out)
	}

	dbBytes := []byte("AKIAIOSFODNN7EXAMPLE")
	masked := redactArtifact("opencode/storage/opencode.db", dbBytes)
	if len(masked) != len(dbBytes) {
		t.Errorf(".db path must keep length: got %d, want %d", len(masked), len(dbBytes))
	}
	if string(masked) == string(dbBytes) {
		t.Error(".db path: secret survived in-place masking")
	}
}
