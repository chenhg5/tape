// Package llm runs prompts through LLMs the user already has: the agent
// CLIs installed on this machine (claude, codex, cursor-agent). No API keys,
// no extra cost beyond the user's existing subscriptions.
package llm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Runner interface {
	Name() string
	Available() bool
	Run(ctx context.Context, prompt string) (string, error)
}

// Runners in preference order.
func Runners() []Runner {
	return []Runner{claudeCLI{}, codexCLI{}, cursorCLI{}}
}

// Pick returns the runner with the given name, or the first available one
// when name is "auto".
func Pick(name string) (Runner, error) {
	if name == "none" {
		return nil, nil
	}
	for _, r := range Runners() {
		if name == "auto" && r.Available() {
			return r, nil
		}
		if r.Name() == name {
			if !r.Available() {
				return nil, fmt.Errorf("llm runner %q is not installed", name)
			}
			return r, nil
		}
	}
	if name == "auto" {
		return nil, nil // no CLI available: caller falls back to template
	}
	return nil, fmt.Errorf("unknown llm runner %q (claude|codex|cursor|none)", name)
}

type claudeCLI struct{}

func (claudeCLI) Name() string    { return "claude" }
func (claudeCLI) Available() bool { return installed("claude") }
func (claudeCLI) Run(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "text")
	cmd.Stdin = strings.NewReader(prompt)
	return capture(cmd)
}

type codexCLI struct{}

func (codexCLI) Name() string    { return "codex" }
func (codexCLI) Available() bool { return installed("codex") }
func (codexCLI) Run(ctx context.Context, prompt string) (string, error) {
	// codex exec interleaves progress logs with output; --output-last-message
	// gives us just the final answer.
	tmp, err := os.CreateTemp("", "tape-codex-*.txt")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--skip-git-repo-check", "--output-last-message", tmp.Name(), "-")
	cmd.Stdin = strings.NewReader(prompt)
	if _, err := capture(cmd); err != nil {
		return "", err
	}
	out, err := os.ReadFile(tmp.Name())
	return strings.TrimSpace(string(out)), err
}

type cursorCLI struct{}

func (cursorCLI) Name() string    { return "cursor" }
func (cursorCLI) Available() bool { return installed("cursor-agent") }
func (cursorCLI) Run(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "cursor-agent", "-p", "--output-format", "text")
	cmd.Stdin = strings.NewReader(prompt)
	return capture(cmd)
}

func installed(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func capture(cmd *exec.Cmd) (string, error) {
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", cmd.Args[0], err, firstLines(errBuf.String(), 3))
	}
	return strings.TrimSpace(out.String()), nil
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(strings.TrimSpace(s), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " / ")
}
