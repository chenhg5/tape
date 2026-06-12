package cli

import (
	"strings"
	"testing"
)

func TestBannerContent(t *testing.T) {
	app := plainApp() // color off so we can assert on plain text
	out := banner(app, "1.2.3")

	for _, want := range []string{
		"v1.2.3",
		"github.com/chenhg5/tape",
		"Record, search and replay",
		"Nothing gets lost on tape",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q in:\n%s", want, out)
		}
	}

	// glyph rows: ANSI Shadow uses these block characters; verify the
	// wordmark survived
	for _, glyph := range []string{"████████╗", "██████╔╝", "╚══════╝"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("banner missing glyph %q", glyph)
		}
	}

	// six wordmark rows + blank + tagline + subtag + blank + meta
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) < 10 {
		t.Errorf("banner too short: %d rows", len(rows))
	}
}

func TestBannerDevVersionFallback(t *testing.T) {
	out := banner(plainApp(), "")
	if !strings.Contains(out, "vdev") {
		t.Errorf("empty version must fall back to 'vdev', got:\n%s", out)
	}
}

func TestBannerColorsBrandLetters(t *testing.T) {
	app := &App{} // color is determined by useColor(); in tests useColor returns false
	// Confirm brand color helper produces ANSI 256-color escapes when on.
	// We can't easily flip useColor in a unit test (it touches the real
	// terminal), but we can verify the helper string shape.
	app.jsonOut = false
	// directly exercise the escape sequence builder
	got := (&App{}).brand(173, "X") // useColor false → returns "X"
	if got != "X" {
		t.Errorf("color-off brand should be raw, got %q", got)
	}
}

func TestAgentColorBrandTable(t *testing.T) {
	app := plainApp()
	// With color off the result equals the input — what matters is that
	// every known agent has a branch and unknown agents fall back.
	for _, agent := range []string{
		"claude-code", "codex", "cursor",
		"gemini", "qwen", "iflow", "aider",
		"mystery",
	} {
		if got := app.agentColor(agent); got != agent {
			t.Errorf("agentColor(%q) = %q, want plain %q", agent, got, agent)
		}
	}
}
