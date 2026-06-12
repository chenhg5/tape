package restore

// Memory injection: write a handoff/transcript markdown file into the
// project, then make the target agent auto-load it by appending an
// `@`-reference into that agent's project-memory file. After this, the
// user just opens the target agent in the project — no need to ask it
// to read anything.
//
// Memory file per agent (project-scoped, in cwd):
//
//   claude-code → CLAUDE.md   (Claude Code reads this on startup)
//   codex       → AGENTS.md   (Codex follows the AGENTS.md convention)
//   cursor      → AGENTS.md   (cursor-agent also reads AGENTS.md)
//   gemini      → GEMINI.md   (gemini-cli loads <Agent>.md from the cwd)
//   qwen        → QWEN.md     (qwen-code likewise)
//   iflow       → IFLOW.md    (iflow's docs explicitly call this out)
//   aider       → CONVENTIONS.md (aider's --read default convention file)
//
// Why not .cursor/rules/? It works, but AGENTS.md is the cross-agent
// standard and saves us from per-agent path knowledge. If the user keeps
// both, AGENTS.md still wins for cursor-agent because rules are scoped
// to the editor's IDE side.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// memoryFile returns the project-memory filename for an agent. The empty
// string means "we don't know a memory convention for this agent" —
// callers should fall back to a plain transcript in that case.
func memoryFile(agent string) string {
	switch agent {
	case "claude-code":
		return "CLAUDE.md"
	case "codex", "cursor":
		return "AGENTS.md"
	case "gemini":
		return "GEMINI.md"
	case "qwen":
		return "QWEN.md"
	case "iflow":
		return "IFLOW.md"
	case "aider":
		return "CONVENTIONS.md"
	default:
		return ""
	}
}

// InjectMemory writes the handoff (transcript or brief) to handoffPath
// and appends a fenced reference block to the target agent's memory
// file in projectRoot. Idempotent: re-running with the same handoffPath
// replaces the previous tape block instead of stacking them.
//
// Returns the absolute paths of the two files written.
func InjectMemory(projectRoot, agent, handoffPath, body string) (handoffAbs, memoryAbs string, err error) {
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	handoffAbs = handoffPath
	if !filepath.IsAbs(handoffAbs) {
		handoffAbs = filepath.Join(projectRoot, handoffAbs)
	}
	if err := os.WriteFile(handoffAbs, []byte(body), 0o600); err != nil {
		return "", "", err
	}

	mf := memoryFile(agent)
	if mf == "" {
		return handoffAbs, "", nil // no convention → caller already has the file
	}
	memoryAbs = filepath.Join(projectRoot, mf)

	rel, err := filepath.Rel(projectRoot, handoffAbs)
	if err != nil {
		rel = handoffAbs
	}
	block := buildMemoryBlock(rel)

	existing, _ := os.ReadFile(memoryAbs)
	newBody := mergeMemoryBlock(string(existing), block)
	if err := os.WriteFile(memoryAbs, []byte(newBody), 0o644); err != nil {
		return "", "", err
	}
	return handoffAbs, memoryAbs, nil
}

const (
	memoryStartMarker = "<!-- tape:handoff:start -->"
	memoryEndMarker   = "<!-- tape:handoff:end -->"
)

func buildMemoryBlock(relPath string) string {
	return fmt.Sprintf(`%s
## Resumed session context

The conversation up to this point is captured in [%s](%s). Read it
first, then continue the work the user left off.
%s`, memoryStartMarker, relPath, relPath, memoryEndMarker)
}

// mergeMemoryBlock either replaces the prior tape block (idempotent) or
// appends a fresh one to existing memory contents. A trailing newline is
// guaranteed so the block doesn't end up jammed against earlier text.
func mergeMemoryBlock(existing, block string) string {
	start := strings.Index(existing, memoryStartMarker)
	end := strings.Index(existing, memoryEndMarker)
	if start >= 0 && end > start {
		before := strings.TrimRight(existing[:start], "\n")
		after := strings.TrimLeft(existing[end+len(memoryEndMarker):], "\n")
		joined := before
		if before != "" {
			joined += "\n\n"
		}
		joined += block
		if after != "" {
			joined += "\n\n" + after
		}
		return joined + "\n"
	}
	existing = strings.TrimRight(existing, "\n")
	if existing == "" {
		return block + "\n"
	}
	return existing + "\n\n" + block + "\n"
}
