package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubBin drops an executable shell script named bin into a temp dir that
// is prepended to PATH, simulating an installed agent CLI.
func stubBin(t *testing.T, dir, bin, script string) {
	t.Helper()
	p := filepath.Join(dir, bin)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// withPath puts dir first on PATH while keeping system dirs so that the
// stub scripts can still call cat/printf.
func withPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

// withEmptyPath hides every binary, simulating a machine with no agent CLI.
func withEmptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestPickNone(t *testing.T) {
	r, err := Pick("none")
	if r != nil || err != nil {
		t.Errorf("none: %v %v", r, err)
	}
}

func TestPickUnknown(t *testing.T) {
	if _, err := Pick("gpt9"); err == nil {
		t.Error("unknown runner must error")
	}
}

func TestPickAutoEmptyPath(t *testing.T) {
	withEmptyPath(t)
	r, err := Pick("auto")
	if r != nil || err != nil {
		t.Errorf("auto with nothing installed: %v %v", r, err)
	}
}

func TestPickNamedNotInstalled(t *testing.T) {
	withEmptyPath(t)
	if _, err := Pick("claude"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("want not-installed error, got %v", err)
	}
}

func TestClaudeRunnerEchoesStdin(t *testing.T) {
	dir := t.TempDir()
	stubBin(t, dir, "claude", `cat -; printf ' [args: %s]' "$*"`)
	withPath(t, dir)

	r, err := Pick("claude")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), "PROMPT-TEXT")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PROMPT-TEXT") {
		t.Errorf("prompt not passed via stdin: %q", out)
	}
	if !strings.Contains(out, "-p") || !strings.Contains(out, "--output-format text") {
		t.Errorf("expected claude print-mode flags, got %q", out)
	}
}

func TestPickAutoPrefersClaude(t *testing.T) {
	dir := t.TempDir()
	stubBin(t, dir, "claude", "exit 0")
	stubBin(t, dir, "codex", "exit 0")
	withPath(t, dir)
	r, err := Pick("auto")
	if err != nil || r == nil || r.Name() != "claude" {
		t.Errorf("auto pick: %v %v", r, err)
	}
}

func TestCodexRunnerUsesLastMessageFile(t *testing.T) {
	dir := t.TempDir()
	// emulate `codex exec --output-last-message <file>`: write answer to the
	// file given after the flag, spam logs to stdout
	stubBin(t, dir, "codex", `
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--output-last-message" ]; then out="$a"; fi
  prev="$a"
done
echo "noisy progress log"
printf 'FINAL ANSWER' > "$out"`)
	withPath(t, dir)

	r, err := Pick("codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), "whatever")
	if err != nil {
		t.Fatal(err)
	}
	if out != "FINAL ANSWER" {
		t.Errorf("got %q, want clean final answer without logs", out)
	}
}

func TestRunnerFailureIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	stubBin(t, dir, "claude", `echo "quota exceeded" >&2; exit 1`)
	withPath(t, dir)

	r, _ := Pick("claude")
	_, err := r.Run(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("stderr not surfaced: %v", err)
	}
}

func TestFirstLines(t *testing.T) {
	if got := firstLines("a\nb\nc\nd", 2); got != "a / b" {
		t.Errorf("got %q", got)
	}
	if got := firstLines("single", 3); got != "single" {
		t.Errorf("got %q", got)
	}
}
