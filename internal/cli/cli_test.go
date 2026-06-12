package cli

import (
	"os"
	"path/filepath"
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
		"codex/019ea0af-3d6a-7393-8ccf-a4ae49f116c3": "codex/019ea0af",
		"claude-code/7dd2afaf-1234-5678":             "claude-code/7dd2afaf",
		"short":                                      "short",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
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
	sub := &cobra.Command{Use: "backup", Short: "backup things"}
	push := &cobra.Command{Use: "push", Short: "push it", Example: "  tape backup push"}
	push.Flags().Bool("dry-run", false, "preview")
	push.Flags().String("remote", "", "git remote URL")
	sub.AddCommand(push)
	root.AddCommand(sub, &cobra.Command{Use: "help"})

	s := describe(root)
	if len(s.Subcommands) != 1 || s.Subcommands[0].Name != "backup" {
		t.Fatalf("help command must be hidden, got %+v", s.Subcommands)
	}
	p := s.Subcommands[0].Subcommands[0]
	if p.Name != "push" || p.Example == "" {
		t.Errorf("push schema: %+v", p)
	}
	names := map[string]string{}
	for _, f := range p.Flags {
		names[f.Name] = f.Type
	}
	if names["--dry-run"] != "bool" || names["--remote"] != "string" {
		t.Errorf("flags: %v", names)
	}
}

func TestScanArchiveFindsPlantedSecret(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "codex", "p", "s1")
	os.MkdirAll(filepath.Join(sessDir, "raw"), 0o700)
	os.WriteFile(filepath.Join(sessDir, "session.json"),
		[]byte(`{"text":"token AKIAIOSFODNN7EXAMPLE here"}`), 0o600)
	// raw dir is intentionally skipped by the scanner
	os.WriteFile(filepath.Join(sessDir, "raw", "x.jsonl"),
		[]byte("AKIAIOSFODNN7EXAMPLE"), 0o600)

	findings, err := scanArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding (raw skipped), got %d: %+v", len(findings), findings)
	}
	if findings[0].Rule != "aws-access-key" || !strings.Contains(findings[0].Path, "session.json") {
		t.Errorf("finding: %+v", findings[0])
	}
}

func TestScanArchiveMissingDir(t *testing.T) {
	findings, err := scanArchive(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(findings) != 0 {
		t.Errorf("missing dir must be clean: %v %v", findings, err)
	}
}
