package qoder

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

// Write materializes a session in Qoder's native ChatRecord format
// (inherited from the Qwen Code schema) so the next `qodercli` invocation
// lists it under the project. Qoder has a real resume flag, so the
// returned command launches the agent directly inside the conversation.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	projDir := filepath.Join(s.root, "projects", projectSlug(cwd))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	sid := "tape-" + scan.UUIDv4()[:8]
	path := filepath.Join(projDir, sid+".jsonl")

	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	now := time.Now().UTC()

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	parent := ""
	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		ts := m.Timestamp
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Millisecond)
		}
		uuid := scan.UUIDv4()
		typ := "user"
		role := "user"
		if m.Role == model.RoleAssistant {
			typ = "assistant"
			role = "model"
		}
		text := m.Text
		if i == 0 && m.Role == model.RoleUser {
			text = "[tape] " + title + "\n\n" + text
		}
		rec := map[string]any{
			"uuid":       uuid,
			"parentUuid": nullable(parent),
			"sessionId":  sid,
			"timestamp":  ts.Format(time.RFC3339Nano),
			"type":       typ,
			"cwd":        cwd,
			"message":    map[string]any{"role": role, "parts": []any{map[string]any{"text": text}}},
		}
		if m.Role == model.RoleAssistant && sess.Model != "" {
			rec["model"] = sess.Model
		}
		if sess.GitBranch != "" {
			rec["gitBranch"] = sess.GitBranch
		}
		if err := enc.Encode(rec); err != nil {
			return ports.WriteResult{}, err
		}
		parent = uuid
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && qodercli -r %s", cwd, sid),
		TargetFile:    path,
	}, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// projectSlug picks the directory name for ~/.qoder/projects/<slug>/.
// Qoder uses a path-slug ('/'→'-') under projects/, which keeps file
// listings human-readable. We mirror that so a `ls ~/.qoder/projects`
// shows recognizable paths instead of opaque hashes.
func projectSlug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		switch r {
		case '/', '\\', ':', ' ', '.':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
