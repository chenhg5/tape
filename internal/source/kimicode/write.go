package kimicode

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

// Write materializes a session in kimi-code's native on-disk layout
// (sessions/<workDirKey>/<sessionId>/{state.json, agents/main/wire.jsonl})
// so the next `kimi --session <id>` invocation resumes it natively.
// kimi-code has a real --session flag, so the returned command takes
// the user straight back into the conversation.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sid := scan.UUIDv4()
	wdKey := workDirKey(cwd)
	sessDir := filepath.Join(s.root, "sessions", wdKey, sid)
	wireDir := filepath.Join(sessDir, "agents", "main")
	if err := os.MkdirAll(wireDir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}

	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	now := time.Now().UTC()

	// state.json — kimi reads title/workDir/timestamps to render the
	// session row in its picker. The model field is optional but nice
	// to have when the user wants to swap models later.
	stateOut := map[string]any{
		"title":     "[tape] " + title,
		"workDir":   cwd,
		"createdAt": now.Format(time.RFC3339Nano),
		"updatedAt": now.Format(time.RFC3339Nano),
	}
	if sess.Model != "" {
		stateOut["model"] = sess.Model
	}
	stateBytes, err := json.MarshalIndent(stateOut, "", "  ")
	if err != nil {
		return ports.WriteResult{}, err
	}
	if err := os.WriteFile(filepath.Join(sessDir, "state.json"), stateBytes, 0o600); err != nil {
		return ports.WriteResult{}, err
	}

	// wire.jsonl — one envelope per event. Each turn is a TurnBegin
	// carrying the user input followed by ContentPart events for the
	// assistant reply. We don't synthesize tool traffic because kimi's
	// ToolResult records would need a matching ToolCall id, and
	// fabricating one risks confusing kimi's tool-state replay.
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	emit := func(typ string, payload any) error {
		return enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"method":  "event",
			"params":  map[string]any{"type": typ, "payload": payload},
		})
	}

	first := true
	for _, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		text := m.Text
		if first && m.Role == model.RoleUser {
			text = "[tape] " + title + "\n\n" + text
			first = false
		}
		if m.Role == model.RoleUser {
			if err := emit("TurnBegin", map[string]any{"user_input": text}); err != nil {
				return ports.WriteResult{}, err
			}
			continue
		}
		// assistant — ContentPart with a text payload.
		if err := emit("ContentPart", map[string]any{"type": "text", "text": text}); err != nil {
			return ports.WriteResult{}, err
		}
	}

	wirePath := filepath.Join(wireDir, "wire.jsonl")
	if err := os.WriteFile(wirePath, []byte(sb.String()), 0o600); err != nil {
		return ports.WriteResult{}, err
	}
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && kimi --session %s", cwd, sid),
		TargetFile:    wirePath,
	}, nil
}

// workDirKey is the bucket directory kimi-code groups sessions under.
// We use a stable sha256 truncation of cwd; the actual algorithm
// kimi-code uses for new sessions isn't public, but Load() reads
// state.json's workDir for the real cwd, so any deterministic key
// works as long as it's filesystem-safe and unique per cwd.
func workDirKey(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:12]
}
