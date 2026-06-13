package restore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectMemoryCreatesBothFiles(t *testing.T) {
	root := t.TempDir()
	handoffPath := ".tape-handoff.md"
	body := "# transcript\n\nhello world"

	h, m, err := InjectMemory(root, "claude-code", handoffPath, body)
	if err != nil {
		t.Fatal(err)
	}
	if h != filepath.Join(root, handoffPath) {
		t.Errorf("handoff path: %s", h)
	}
	if m != filepath.Join(root, "CLAUDE.md") {
		t.Errorf("memory path: %s", m)
	}

	got, _ := os.ReadFile(h)
	if string(got) != body {
		t.Errorf("handoff body altered: %q", got)
	}

	mem, _ := os.ReadFile(m)
	if !strings.Contains(string(mem), memoryStartMarker) ||
		!strings.Contains(string(mem), memoryEndMarker) {
		t.Errorf("memory file missing markers:\n%s", mem)
	}
	if !strings.Contains(string(mem), ".tape-handoff.md") {
		t.Errorf("memory file should reference the handoff: %s", mem)
	}
}

func TestInjectMemoryIsIdempotent(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "AGENTS.md")
	_ = os.WriteFile(mem, []byte("# Project conventions\n\nUse tabs.\n"), 0o600)

	for i := 0; i < 3; i++ {
		if _, _, err := InjectMemory(root, "codex", ".tape-handoff.md", "body"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(mem)
	s := string(got)
	if strings.Count(s, memoryStartMarker) != 1 {
		t.Errorf("expected exactly one tape block, got %d:\n%s",
			strings.Count(s, memoryStartMarker), s)
	}
	if !strings.Contains(s, "Use tabs.") {
		t.Errorf("user content was lost:\n%s", s)
	}
	// User content should come before the tape block (we append, not prepend).
	if i := strings.Index(s, "Use tabs."); i < 0 || i > strings.Index(s, memoryStartMarker) {
		t.Errorf("user content order wrong:\n%s", s)
	}
}

func TestInjectMemoryPhaseAAgentsAllResolveToMemoryFile(t *testing.T) {
	// Each Phase-A agent must land in its conventional memory file —
	// regression guard so we don't silently drop one when we touch
	// memoryFile() later.
	want := map[string]string{
		"gemini":      "GEMINI.md",
		"qwen":        "QWEN.md",
		"iflow":       "IFLOW.md",
		"aider":       "CONVENTIONS.md",
		"antigravity": "GEMINI.md", // Antigravity inherits gemini-cli's convention
		"qoder":       "AGENTS.md", // qoder is in the AGENTS.md family
		"mimocode":    "MEMORY.md", // MiMo Code's project memory convention
		"kimi-code":   "AGENTS.md", // Kimi Code follows the AGENTS.md hierarchy
	}
	for agent, expected := range want {
		root := t.TempDir()
		_, m, err := InjectMemory(root, agent, ".tape-handoff.md", "body")
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if filepath.Base(m) != expected {
			t.Errorf("%s memory file = %s, want %s", agent, m, expected)
		}
	}
}

func TestInjectMemoryUnknownAgentSkipsMemoryFile(t *testing.T) {
	root := t.TempDir()
	h, m, err := InjectMemory(root, "unknown-agent", ".tape-handoff.md", "body")
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Error("handoff path should still be returned")
	}
	if m != "" {
		t.Errorf("unknown agent should not get a memory file, got %s", m)
	}
}
