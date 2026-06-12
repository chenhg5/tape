package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

// Write materializes a session in Claude Code's native format so it can be
// resumed with `claude --resume`. Best-effort by design: text dialogue is
// carried over; tool traffic is not replayed (Claude has no matching tool
// state anyway). Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (string, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dir := filepath.Join(s.dir, projectDirName(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	id := scan.UUIDv4()
	path := filepath.Join(dir, id+".jsonl")

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	enc.Encode(map[string]any{
		"type": "ai-title", "aiTitle": "[tape] " + title, "sessionId": id,
	})

	parent := ""
	now := time.Now().UTC()
	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		uuid := scan.UUIDv4()
		ts := m.Timestamp
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Millisecond)
		}
		var message any
		if m.Role == model.RoleUser {
			message = map[string]any{"role": "user", "content": m.Text}
		} else {
			message = map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": m.Text}}}
		}
		line := map[string]any{
			"parentUuid":  nullable(parent),
			"isSidechain": false,
			"userType":    "external",
			"cwd":         cwd,
			"sessionId":   id,
			"gitBranch":   sess.GitBranch,
			"type":        string(m.Role),
			"message":     message,
			"uuid":        uuid,
			"timestamp":   ts.Format(time.RFC3339Nano),
		}
		if err := enc.Encode(line); err != nil {
			return "", err
		}
		parent = uuid
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("cd %s && claude --resume %s", cwd, id), nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// projectDirName mirrors Claude Code's project directory naming:
// "/root/code/tape" -> "-root-code-tape" ('/' and '.' become '-').
func projectDirName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r == '/' || r == '.' || r == '\\' || r == ':' || r == ' ' {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
