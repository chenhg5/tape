package codex

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

// Write materializes a session in Codex's rollout format so it can be
// resumed with `codex resume`. Text dialogue only, best-effort (see the
// claude-code writer for rationale). Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	id := scan.UUIDv7()
	now := time.Now().UTC()
	dir := filepath.Join(s.dir, now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), id))

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	ts := now.Format("2006-01-02T15:04:05.000Z")
	enc.Encode(map[string]any{
		"timestamp": ts,
		"type":      "session_meta",
		"payload":   s.sessionMetaPayload(id, ts, cwd),
	})
	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		kind := "input_text"
		if m.Role == model.RoleAssistant {
			kind = "output_text"
		}
		mts := m.Timestamp
		if mts.IsZero() {
			mts = now.Add(time.Duration(i) * time.Millisecond)
		}
		line := map[string]any{
			"timestamp": mts.Format(time.RFC3339Nano),
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "message",
				"role":    string(m.Role),
				"content": []map[string]any{{"type": kind, "text": m.Text}},
			},
		}
		if err := enc.Encode(line); err != nil {
			return ports.WriteResult{}, err
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && codex resume %s", cwd, id),
		TargetFile:    path,
	}, nil
}

// sessionMetaPayload builds the session_meta payload. Codex deserializes it
// strictly, so the most robust template is the newest real session on this
// machine (correct cli_version and enum values for this install), with id,
// timestamp and cwd overridden. Falls back to observed field values.
func (s *Source) sessionMetaPayload(id, ts, cwd string) map[string]any {
	payload := map[string]any{
		"id": id, "timestamp": ts, "cwd": cwd,
		"originator": "codex_exec", "cli_version": "0.137.0",
		"source": "exec", "thread_source": "user", "model_provider": "openai",
	}
	files, _ := filepath.Glob(filepath.Join(s.dir, "*", "*", "*", "rollout-*.jsonl"))
	var newest string
	var newestMod time.Time
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(newestMod) {
			newest, newestMod = f, st.ModTime()
		}
	}
	if newest == "" {
		return payload
	}
	f, err := os.Open(newest)
	if err != nil {
		return payload
	}
	defer f.Close()
	var first struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if json.NewDecoder(f).Decode(&first) != nil || first.Type != "session_meta" {
		return payload
	}
	tpl := first.Payload
	tpl["id"], tpl["timestamp"], tpl["cwd"] = id, ts, cwd
	delete(tpl, "git") // stale commit info from the template session
	return tpl
}
