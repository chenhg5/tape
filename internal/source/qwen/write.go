package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Write materializes a session in Qwen Code's native ChatRecord format
// so the next `qwen` invocation in the same project picks it up. Qwen
// Code doesn't expose a public --resume <id> flag; the returned command
// drops the user back into the project so the agent's built-in /chat
// list surfaces "[tape] <title>".
//
// Best-effort by design: textual dialogue is preserved; function-call
// metadata is recorded as Parts but their tool state isn't replayable.
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	hash := projectHash(cwd)
	chatDir := filepath.Join(s.root, "projects", hash, "chats")
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	sid := "tape-" + scan.UUIDv4()[:8]
	path := filepath.Join(chatDir, sid+".jsonl")

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
		// Carry the [tape] marker on the very first user message so a
		// human scrolling Qwen's history sees where this came from
		// without having to dig into metadata.
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
		ResumeCommand: fmt.Sprintf("cd %s && qwen  # restored as \"[tape] %s\"; pick it from /chat list", cwd, title),
		TargetFile:    path,
	}, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// projectHash matches the convention Qwen Code uses for its
// ~/.qwen/projects/<hash>/ directory: sha256(cwd) hex-encoded and
// truncated. The exact algorithm matters less than determinism — the
// session lands under a stable directory the agent will reuse next
// time the same cwd is opened.
func projectHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:12]
}
