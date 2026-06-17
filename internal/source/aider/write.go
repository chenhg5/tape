package aider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// Write materializes a session into aider's native markdown chat
// history format. We append a fresh `# aider chat started at ...` block
// to `<cwd>/.aider.chat.history.md` (the per-project file aider auto-
// loads on the next launch in that directory). aider exposes no
// --resume <id> flag; restarting aider in the project simply re-reads
// the markdown file, so the returned command is just `cd <cwd> && aider`.
//
// We intentionally write to the project file (not the global
// ~/.aider.chat.history.md) so the restored session stays scoped to
// the user's current workspace and doesn't pollute unrelated projects.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	path := filepath.Join(cwd, ".aider.chat.history.md")

	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	now := time.Now().UTC()

	var sb strings.Builder
	// If the file already has content, separate the new block from it.
	// aider's own writer puts a blank line before each new header.
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "# aider chat started at %s\n\n", now.Format("2006-01-02 15:04:05"))
	// One-line provenance marker so the user knows which block tape wrote.
	fmt.Fprintf(&sb, "> [tape] %s\n\n", title)

	for _, m := range sess.Messages {
		if m.Text == "" {
			continue
		}
		switch m.Role {
		case model.RoleUser:
			for _, line := range strings.Split(m.Text, "\n") {
				fmt.Fprintf(&sb, "#### %s\n", line)
			}
			sb.WriteString("\n")
		case model.RoleAssistant:
			sb.WriteString(strings.TrimRight(m.Text, "\n"))
			sb.WriteString("\n\n")
		case model.RoleTool:
			for _, line := range strings.Split(m.Text, "\n") {
				fmt.Fprintf(&sb, "> %s\n", line)
			}
			sb.WriteString("\n")
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return ports.WriteResult{}, err
	}
	defer f.Close()
	if _, err := f.WriteString(sb.String()); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && aider  # restored as a new chat block in .aider.chat.history.md", cwd),
		TargetFile:    path,
	}, nil
}
