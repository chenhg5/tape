package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

// Write materializes a session in Antigravity's native transcript format
// so it shows up under ~/.gemini/antigravity-cli/brain/<convID>/.../
// transcript_full.jsonl. Antigravity supports `agy --conversation <id>`
// as a real resume flag, so the returned command lands the user inside
// the restored conversation directly.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	convID := scan.UUIDv4()
	logs := filepath.Join(s.root, "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	path := filepath.Join(logs, "transcript_full.jsonl")

	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	now := time.Now().UTC()

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	stepIndex := 0
	// Leading USER_INPUT carrying the [tape] marker so the user sees
	// where this conversation came from even before scrolling.
	enc.Encode(map[string]any{
		"step_index": stepIndex,
		"source":     "USER_EXPLICIT",
		"type":       "USER_INPUT",
		"status":     "DONE",
		"created_at": now.Format(time.RFC3339),
		"content":    "[tape] " + title,
	})
	stepIndex++

	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		ts := m.Timestamp
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Millisecond)
		}
		var rec map[string]any
		if m.Role == model.RoleUser {
			rec = map[string]any{
				"step_index": stepIndex,
				"source":     "USER_EXPLICIT",
				"type":       "USER_INPUT",
				"status":     "DONE",
				"created_at": ts.Format(time.RFC3339),
				"content":    m.Text,
			}
		} else {
			rec = map[string]any{
				"step_index": stepIndex,
				"source":     "MODEL",
				"type":       "PLANNER_RESPONSE",
				"status":     "DONE",
				"created_at": ts.Format(time.RFC3339),
				"content":    m.Text,
			}
		}
		if err := enc.Encode(rec); err != nil {
			return ports.WriteResult{}, err
		}
		stepIndex++
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && agy --conversation %s", cwd, convID),
		TargetFile:    path,
	}, nil
}
