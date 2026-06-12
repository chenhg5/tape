package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestPhaseAAgents drives the full sync → search → show → restore loop
// across the four Phase-A agents (Gemini CLI, Qwen Code, iFlow, Aider).
// The point isn't to retest every parser branch — those have unit tests —
// it's to prove these new agents play nicely with every other CLI command,
// so we'd catch wiring regressions (factory registration, agent colors,
// memory-file mapping, etc.) before they reach a user.
func TestPhaseAAgents(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedGemini()
	e.seedQwen()
	e.seedIFlow()
	e.seedAider()
	e.seedOpenCode()

	// sync: every adapter discovers its sessions and the report counts
	// all of them under "archived".
	d := e.mustRun(0, "sync").data(t)
	if got := int(d["archived"].(float64)); got < 5 {
		t.Fatalf("archived = %d, want >= 5 (one per new agent)", got)
	}

	// ls shows every new agent.
	d = e.mustRun(0, "ls", "--dir", "").data(t)
	seen := map[string]bool{}
	for _, raw := range d["sessions"].([]any) {
		item := raw.(map[string]any)
		seen[item["agent"].(string)] = true
	}
	for _, a := range []string{"gemini", "qwen", "iflow", "aider", "opencode"} {
		if !seen[a] {
			t.Errorf("ls missed agent %q (seen=%v)", a, seen)
		}
	}

	// search returns matches from each new agent.
	for query, wantAgent := range map[string]string{
		"webpack":      "gemini",
		"jwt 鉴权":       "qwen",
		"翻译":           "iflow",
		"refactor the": "aider",
		"e2e 测试":       "opencode",
	} {
		d = e.mustRun(0, "search", query, "--dir", "").data(t)
		hits, _ := d["hits"].([]any)
		if len(hits) == 0 {
			t.Errorf("search %q: no hits", query)
			continue
		}
		var found bool
		for _, raw := range hits {
			item := raw.(map[string]any)
			if item["agent"] == wantAgent {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("search %q: no hit for agent %s (got %v)", query, wantAgent, hits)
		}
	}

	// show by sourceID for one of the new agents — proves resolveSessionID
	// + the gemini parser work end-to-end through the CLI.
	d = e.mustRun(0, "show", geminiSessionID).data(t)
	if d["agent"] != "gemini" {
		t.Errorf("show: agent = %v, want gemini", d["agent"])
	}
	if msgs, ok := d["messages"].([]any); !ok || len(msgs) == 0 {
		t.Errorf("show: no messages: %v", d["messages"])
	}

	// restore the qwen session into claude-code via memory strategy:
	// the QWEN→Claude bridge writes a transcript file and weaves it
	// into CLAUDE.md inside the project root.
	out := filepath.Join(e.home, "qwen-handoff.md")
	mem := filepath.Join(e.home, "CLAUDE.md")
	e.mustRun(0,
		"restore", qwenSessionID,
		"--to", "claude-code",
		"--strategy", "transcript",
		"--output", out,
	)
	got := readFile(t, out)
	if !strings.Contains(got, "实现 jwt 鉴权") {
		t.Errorf("transcript missing original user text:\n%s", got[:min(200, len(got))])
	}

	// And the memory strategy puts a CLAUDE.md beside it with a
	// fenced @-reference back to the handoff.
	e.mustRun(0,
		"restore", qwenSessionID,
		"--to", "claude-code",
		"--strategy", "memory",
		"--output", out,
	)
	memBody := readFile(t, mem)
	if !strings.Contains(memBody, "<!-- tape:handoff:start -->") {
		t.Errorf("memory injection didn't leave a marker in CLAUDE.md:\n%s", memBody)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
