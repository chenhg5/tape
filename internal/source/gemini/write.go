package gemini

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

// Write materializes a session in Gemini CLI's native ChatRecording
// format so the next `gemini` invocation in the same project lists it
// in its built-in history picker. Gemini CLI has no public --resume
// <id> flag, so the returned command just launches `gemini` inside the
// original cwd; the caller's UI explains that the session shows up as
// "[tape] <title>" in the agent's own /chat list.
//
// Best-effort by design: textual dialogue is preserved; tool traffic
// is not replayed (Gemini's tool state doesn't survive cross-process).
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	hash := projectHash(cwd)
	chats := filepath.Join(s.root, "tmp", hash, "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	now := time.Now().UTC()
	suffix := scan.UUIDv4()[:8]
	id := suffix
	path := filepath.Join(chats, fmt.Sprintf("session-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), suffix))

	title := sess.Title
	if title == "" {
		title = "restored session"
	}

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	// Leading metadata line — matches gemini-cli's ChatRecordingService.
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
		ResumeCommand: fmt.Sprintf("cd %s && gemini  # open the session picker; look for \"[tape] %s\"", cwd, title),
		TargetFile:    path,
	}, nil
}

// projectHash matches the directory name gemini-cli derives from cwd
// when it lays out ~/.gemini/tmp/<hash>/chats/. It's a sha256 of the
// absolute path, truncated to 12 hex chars — short enough to round-trip
// in error messages, long enough to never collide in practice.
func projectHash(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:12]
}
