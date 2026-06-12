package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	r := e.mustRun(10, "restore", "@last", "--to", "codex", "--dry-run")
	d := r.data(t)
	if d["strategy"] != "native" || d["target"] != "codex" {
		t.Errorf("plan: %v", d)
	}
	// dry run must not create any codex session beyond the fixture
	files, _ := filepath.Glob(filepath.Join(e.home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if len(files) != 1 {
		t.Errorf("dry run wrote files: %v", files)
	}
}

// Claude -> Codex native restore: the written rollout must be a loadable
// codex session carrying the original dialogue.
func TestRestoreNativeClaudeToCodex(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "restore", "claude-code/7dd2afaf", "--to", "codex").data(t)
	resume, _ := d["resume_command"].(string)
	if !strings.Contains(resume, "codex resume ") {
		t.Fatalf("resume_command = %q", resume)
	}

	files, _ := filepath.Glob(filepath.Join(e.home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if len(files) != 2 { // fixture + restored
		t.Fatalf("rollout files: %v", files)
	}
	var restored string
	for _, f := range files {
		if !strings.Contains(f, codexSessionID) {
			restored = f
		}
	}
	data, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{`"session_meta"`, "为什么不用 OAuth2", "stateless token"} {
		if !strings.Contains(content, want) {
			t.Errorf("restored rollout missing %q", want)
		}
	}
	// session_meta must inherit cli_version from the real session on this machine
	if !strings.Contains(content, `"cli_version":"0.137.0"`) {
		t.Errorf("cli_version not templated from the fixture session:\n%s", firstLine(content))
	}

	// the restored session is itself syncable: round trip through the archive
	d = e.mustRun(0, "sync").data(t)
	if d["archived"] != float64(1) {
		t.Errorf("restored session not picked up by sync: %v", d)
	}
}

// Codex -> Claude native restore.
func TestRestoreNativeCodexToClaude(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "restore", "codex/019ea0af", "--to", "claude-code").data(t)
	if !strings.Contains(d["resume_command"].(string), "claude --resume ") {
		t.Fatalf("resume_command = %v", d["resume_command"])
	}
	files, _ := filepath.Glob(filepath.Join(e.home, ".claude", "projects", "*", "*.jsonl"))
	if len(files) != 2 {
		t.Fatalf("project files: %v", files)
	}
	var restored string
	for _, f := range files {
		if !strings.Contains(f, claudeSessionID) {
			restored = f
		}
	}
	data, _ := os.ReadFile(restored)
	if !strings.Contains(string(data), "帮我优化构建速度") {
		t.Error("dialogue lost in claude restore")
	}
}

// brief strategy with --llm none must deterministically produce the
// template handoff document.
func TestRestoreBriefTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	out := filepath.Join(t.TempDir(), "handoff.md")
	d := e.mustRun(0, "restore", "codex/019ea0af", "--to", "cursor",
		"--strategy", "brief", "--llm", "none", "--output", out).data(t)
	if d["method"] != "template" || d["strategy"] != "brief" {
		t.Errorf("result: %v", d)
	}
	if !strings.Contains(d["start_command"].(string), "cursor-agent") {
		t.Errorf("start_command = %v", d["start_command"])
	}
	doc, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Handoff", "帮我优化构建速度", "codex/" + codexSessionID} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("handoff missing %q", want)
		}
	}
}

func TestRestoreErrors(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// unknown target agent: usage error. (Use a clearly-invented slug —
	// the supported agent list grows over time and recycling real names
	// turns this guard rail into a tripwire.)
	if r := e.run("restore", "codex/019ea0af", "--to", "non-existent-agent"); r.code != 2 {
		t.Errorf("unknown agent: exit %d, want 2", r.code)
	}
	// missing session: structured not_found error
	r := e.run("restore", "codex/ffffffff", "--to", "claude-code")
	if r.code != 1 {
		t.Errorf("missing session: exit %d, want 1", r.code)
	}
	if r.errJSON(t)["error"] != "not_found" {
		t.Errorf("error = %v", r.errJSON(t))
	}
	// cursor has no native writer: explicit strategy must be rejected
	if r := e.run("restore", "codex/019ea0af", "--to", "cursor", "--strategy", "native"); r.code != 2 {
		t.Errorf("native to cursor: exit %d, want 2", r.code)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
