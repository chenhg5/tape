// Package restore turns an archived session into something a different
// agent can continue from. The brief strategy produces a handoff document;
// native session rewriting lives with each source's SessionWriter.
package restore

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/llm"
)

const transcriptBudget = 24_000 // chars of transcript handed to the LLM

// Brief generates a handoff document for s. With a runner it asks an LLM to
// write the brief; without one it falls back to a deterministic template.
func Brief(ctx context.Context, s *model.Session, runner llm.Runner) (doc string, method string, err error) {
	if runner != nil {
		out, err := runner.Run(ctx, briefPrompt(s))
		if err == nil && strings.TrimSpace(out) != "" {
			return out, "llm:" + runner.Name(), nil
		}
		// LLM failure degrades to the template rather than failing the restore
	}
	return templateBrief(s), "template", nil
}

// Transcript renders the entire conversation verbatim as Markdown. No
// summarization, no LLM call: the next agent gets every user request,
// every assistant reply and every tool call exactly as they happened.
// Ideal when fidelity matters more than token cost.
func Transcript(s *model.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session transcript (restored by tape from %s)\n\n", s.ID)
	fmt.Fprintf(&b, "- **agent:** `%s`  **cwd:** `%s`  **branch:** `%s`\n", s.Agent, s.CWD, s.GitBranch)
	fmt.Fprintf(&b, "- **span:** %s → %s, %d messages\n\n",
		s.StartedAt.Format("2006-01-02 15:04"),
		s.UpdatedAt.Format("2006-01-02 15:04"),
		len(s.Messages))
	b.WriteString("> Continue this work where the conversation below leaves off.\n\n---\n\n")
	for _, m := range s.Messages {
		switch m.Role {
		case model.RoleUser:
			if m.Text != "" {
				fmt.Fprintf(&b, "### 👤 User\n\n%s\n\n", m.Text)
			}
		case model.RoleAssistant:
			if m.Text != "" {
				fmt.Fprintf(&b, "### 🤖 Assistant\n\n%s\n\n", m.Text)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "<details><summary>🔧 %s</summary>\n\n```\n%s\n```\n",
					tc.Name, clip(tc.Input, 4000))
				if tc.Output != "" {
					fmt.Fprintf(&b, "\n**output:**\n\n```\n%s\n```\n", clip(tc.Output, 4000))
				}
				b.WriteString("\n</details>\n\n")
			}
		}
	}
	return b.String()
}

func briefPrompt(s *model.Session) string {
	var b strings.Builder
	b.WriteString(`You are writing a handoff document so that a different AI coding agent can seamlessly continue this coding session. Write in the language the user used. Output ONLY the markdown document, with these sections:

# Handoff
## Goal — what the user is trying to achieve
## State — what has been done, key decisions and WHY (include rejected approaches)
## Files — files created/changed, with one-line notes
## Next — concrete next steps and open questions

Session metadata: agent=` + s.Agent + ` cwd=` + s.CWD + ` branch=` + s.GitBranch + "\n\nTranscript:\n")
	b.WriteString(transcript(s, transcriptBudget))
	return b.String()
}

// transcript renders the dialogue newest-last, trimming oldest content
// first when over budget (recent context matters most for continuation).
func transcript(s *model.Session, budget int) string {
	var parts []string
	for _, m := range s.Messages {
		switch m.Role {
		case model.RoleUser:
			if m.Text != "" {
				parts = append(parts, "USER: "+clip(m.Text, 2000))
			}
		case model.RoleAssistant:
			if m.Text != "" {
				parts = append(parts, "ASSISTANT: "+clip(m.Text, 2000))
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, "TOOL "+tc.Name+": "+clip(tc.Input, 200))
			}
		}
	}
	total := 0
	start := len(parts)
	for i := len(parts) - 1; i >= 0; i-- {
		total += len(parts[i]) + 1
		if total > budget {
			break
		}
		start = i
	}
	out := parts[start:]
	if start > 0 {
		out = append([]string{fmt.Sprintf("[%d earlier messages omitted]", start)}, out...)
	}
	return strings.Join(out, "\n")
}

var pathRe = regexp.MustCompile(`(?:/[\w.\-]+){2,}\.\w{1,8}`)

func templateBrief(s *model.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Handoff (restored by tape from %s)\n\n", s.ID)
	fmt.Fprintf(&b, "- cwd: `%s`  branch: `%s`\n- span: %s — %s, %d messages\n\n",
		s.CWD, s.GitBranch, s.StartedAt.Format("2006-01-02 15:04"), s.UpdatedAt.Format("2006-01-02 15:04"), len(s.Messages))

	b.WriteString("## Goal\n\n")
	if t := firstUser(s); t != "" {
		b.WriteString(clip(t, 1200) + "\n\n")
	}

	b.WriteString("## User requests over the session\n\n")
	n := 0
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			fmt.Fprintf(&b, "- %s\n", clip(firstLine(m.Text), 160))
			if n++; n >= 15 {
				b.WriteString("- …\n")
				break
			}
		}
	}

	if files := touchedFiles(s); len(files) > 0 {
		b.WriteString("\n## Files mentioned\n\n")
		for _, f := range files {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
	}

	b.WriteString("\n## Last assistant message\n\n")
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == model.RoleAssistant && s.Messages[i].Text != "" {
			b.WriteString(clip(s.Messages[i].Text, 2400) + "\n")
			break
		}
	}
	return b.String()
}

func touchedFiles(s *model.Session) []string {
	set := map[string]struct{}{}
	for _, m := range s.Messages {
		for _, tc := range m.ToolCalls {
			for _, p := range pathRe.FindAllString(tc.Input, -1) {
				set[p] = struct{}{}
			}
		}
	}
	files := make([]string, 0, len(set))
	for f := range set {
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) > 30 {
		files = files[:30]
	}
	return files
}

func firstUser(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
