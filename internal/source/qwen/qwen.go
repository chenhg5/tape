// Package qwen reads Qwen Code CLI sessions from
// ~/.qwen/projects/<project-hash>/chats/<sessionId>.jsonl.
//
// Qwen Code originated as a Gemini CLI fork but moved to a much richer
// JSONL schema (ChatRecord) more similar to Claude Code's:
//
//	{
//	  "uuid": "<message-uuid>",
//	  "parentUuid": "<parent-uuid or null>",
//	  "sessionId": "<session-uuid>",
//	  "timestamp": "2026-06-12T10:00:00.000Z",
//	  "type": "user" | "assistant" | "tool_result" | "system",
//	  "subtype": "...",                  // optional, e.g. chat_compression
//	  "cwd": "/path/to/project",
//	  "version": "x.y.z",
//	  "gitBranch": "main",
//	  "message": { "role": "user"|"model"|"tool", "parts": [ ... ] },
//	  "toolCallResult": { ... },
//	  "model": "qwen2.5-coder-30b",
//	  ...
//	}
//
// Parts follow Google's Gemini SDK shape: {text}, {functionCall}, or
// {functionResponse}. We render text directly and surface tool calls as
// model.ToolCall entries. Unknown record shapes are skipped — the raw
// jsonl is always kept verbatim by the archive layer.
package qwen

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

const agentName = "qwen"

type Source struct {
	root string // ~/.qwen
}

func New(home string) *Source { return &Source{root: filepath.Join(home, ".qwen")} }

func (s *Source) Name() string { return agentName }

// Detect succeeds when ~/.qwen/projects exists (which is the case after
// the first `qwen` invocation that records a session).
func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.root); err != nil {
		return false, "", nil
	}
	for _, sub := range []string{"projects", "chats"} {
		if _, err := os.Stat(filepath.Join(s.root, sub)); err == nil {
			return true, s.root, nil
		}
	}
	return false, "", nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	patterns := []string{
		filepath.Join(s.root, "projects", "*", "chats", "*.jsonl"),
		filepath.Join(s.root, "projects", "*", "*.jsonl"),
		filepath.Join(s.root, "chats", "*.jsonl"),
	}
	seen := map[string]bool{}
	var refs []ports.SessionRef
	for _, p := range patterns {
		files, _ := filepath.Glob(p)
		for _, f := range files {
			if seen[f] {
				continue
			}
			seen[f] = true
			st, err := os.Stat(f)
			if err != nil || st.Size() == 0 {
				continue
			}
			if !since.IsZero() && st.ModTime().Before(since) {
				continue
			}
			refs = append(refs, ports.SessionRef{
				Agent:     agentName,
				SourceID:  strings.TrimSuffix(filepath.Base(f), ".jsonl"),
				Files:     []string{f},
				UpdatedAt: st.ModTime().UTC(),
			})
		}
	}
	return refs, nil
}

// chatRecord is the superset of fields we read from a Qwen ChatRecord.
type chatRecord struct {
	UUID       string          `json:"uuid"`
	ParentUUID string          `json:"parentUuid"`
	SessionID  string          `json:"sessionId"`
	Timestamp  string          `json:"timestamp"`
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	CWD        string          `json:"cwd"`
	Version    string          `json:"version"`
	GitBranch  string          `json:"gitBranch"`
	Model      string          `json:"model"`
	Message    *contentBlock   `json:"message"`
	ToolResult *toolCallResult `json:"toolCallResult"`
	AgentName  string          `json:"agentName"`
}

// contentBlock matches Gemini SDK Content: {role, parts[]}.
type contentBlock struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string          `json:"text"`
	FunctionCall     *functionCall   `json:"functionCall"`
	FunctionResponse *functionResp   `json:"functionResponse"`
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResp struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type toolCallResult struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	CallID      string          `json:"callId"`
	Args        json.RawMessage `json:"args"`
	Result      json.RawMessage `json:"result"`
	Status      string          `json:"status"`
}

func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	f, err := os.Open(ref.Files[0])
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sess := &model.Session{
		ID:       agentName + "/" + ref.SourceID,
		Agent:    agentName,
		SourceID: ref.SourceID,
		Meta:     map[string]string{},
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		var rec chatRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if rec.UUID == "" {
			continue
		}
		// system records are append-only events (chat_compression,
		// slash_command, ui_telemetry, rewind, ...) — skip them from
		// the dialogue replay but keep the raw line on disk.
		if rec.Type == "system" {
			continue
		}
		msg, ok := convertMessage(rec, raw)
		if !ok {
			continue
		}
		sess.Messages = append(sess.Messages, msg)

		if rec.CWD != "" {
			sess.CWD = rec.CWD
		}
		if rec.GitBranch != "" {
			sess.GitBranch = rec.GitBranch
		}
		if rec.Model != "" {
			sess.Model = rec.Model
		}
		if rec.Version != "" {
			sess.Meta["cli_version"] = rec.Version
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(sess.Messages) == 0 {
		return nil, nil
	}

	sess.StartedAt = sess.Messages[0].Timestamp
	sess.UpdatedAt = sess.Messages[len(sess.Messages)-1].Timestamp
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = ref.UpdatedAt
	}
	if sess.Title == "" {
		sess.Title = scan.FirstLine(firstUserText(sess), 80)
	}
	return sess, nil
}

func convertMessage(rec chatRecord, raw []byte) (model.Message, bool) {
	role := normalizeRole(rec.Type, rec.Message)
	if role == "" {
		return model.Message{}, false
	}
	ts, _ := time.Parse(time.RFC3339, rec.Timestamp)
	m := model.Message{
		ID:        rec.UUID,
		ParentID:  rec.ParentUUID,
		Role:      role,
		Timestamp: ts.UTC(),
		Raw:       json.RawMessage(append([]byte(nil), raw...)),
	}

	if rec.Message != nil {
		var texts []string
		for _, p := range rec.Message.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
			if p.FunctionCall != nil {
				m.ToolCalls = append(m.ToolCalls, model.ToolCall{
					Name:  p.FunctionCall.Name,
					Input: scan.Truncate(jsonText(p.FunctionCall.Args), scan.MaxToolIO),
				})
			}
			if p.FunctionResponse != nil {
				texts = append(texts, scan.Truncate(jsonText(p.FunctionResponse.Response), scan.MaxToolIO))
			}
		}
		m.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	}

	// Tool result records carry their payload outside `message`.
	if rec.Type == "tool_result" && rec.ToolResult != nil {
		// Attach the result both as the displayed text and as the
		// matching tool-call's Output, so search & UI can find it
		// regardless of which structure they look at.
		out := scan.Truncate(jsonText(rec.ToolResult.Result), scan.MaxToolIO)
		if m.Text == "" {
			m.Text = out
		}
		m.ToolCalls = append(m.ToolCalls, model.ToolCall{
			Name:   rec.ToolResult.Name,
			Input:  scan.Truncate(jsonText(rec.ToolResult.Args), scan.MaxToolIO),
			Output: out,
		})
	}

	if m.Text == "" && len(m.ToolCalls) == 0 {
		return model.Message{}, false
	}
	return m, true
}

func normalizeRole(t string, msg *contentBlock) model.Role {
	switch strings.ToLower(t) {
	case "user":
		return model.RoleUser
	case "assistant", "model", "gemini":
		return model.RoleAssistant
	case "tool", "tool_result", "function":
		return model.RoleTool
	}
	if msg != nil {
		switch strings.ToLower(msg.Role) {
		case "user":
			return model.RoleUser
		case "model", "assistant":
			return model.RoleAssistant
		}
	}
	return ""
}

func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}
