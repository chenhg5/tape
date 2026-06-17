package iflow

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

// Write materializes a session in iFlow's native format (gemini-cli
// derivative, JSONL ChatRecord schema). The file lands in
// ~/.iflow/projects/<hash>/session-<ts>-<id>.jsonl so the next `iflow`
// invocation in the project lists it in the history picker. iFlow
// doesn't expose a public --resume <id> flag; the returned command
// drops the user into the right directory.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	hash := projectHash(cwd)
	dir := filepath.Join(s.root, "projects", hash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	now := time.Now().UTC()
	suffix := scan.UUIDv4()[:8]
	id := suffix
	path := filepath.Join(dir, fmt.Sprintf("session-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), suffix))

	title := sess.Title
	if title == "" {
		title = "restored session"
	}

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	if err := enc.Encode(map[string]any{
		"sessionId":   id,
		"projectHash": hash,
		"startTime":   now.Format(time.RFC3339Nano),
		"kind":        "interactive",
		"directories": []string{cwd},
		"summary":     "[tape] " + title,
	}); err != nil {
		return ports.WriteResult{}, err
	}
	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		ts := m.Timestamp
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Millisecond)
		}
		typ := "user"
		if m.Role == model.RoleAssistant {
			typ = "gemini"
		}
		line := map[string]any{
			"id":        fmt.Sprintf("m%d", i+1),
			"type":      typ,
			"content":   m.Text,
			"timestamp": ts.Format(time.RFC3339Nano),
		}
		if m.Role == model.RoleAssistant && sess.Model != "" {
			line["model"] = sess.Model
		}
		if err := enc.Encode(line); err != nil {
			return ports.WriteResult{}, err
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && iflow  # restored as \"[tape] %s\"; pick it from /chat list", cwd, title),
		TargetFile:    path,
	}, nil
}

func projectHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:12]
}
