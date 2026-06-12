// Package codex reads Codex CLI sessions from
// ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl.
//
// Line types: session_meta (cwd, cli version, model provider, base
// instructions), response_item (the conversation: message / reasoning /
// function_call / function_call_output / custom_tool_call / ...),
// event_msg and turn_context (runtime metadata). Unknown payloads are
// skipped fail-soft and survive in the raw archive copy.
package codex

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

const agentName = "codex"

type Source struct {
	dir string // ~/.codex/sessions
}

func New(home string) *Source {
	return &Source{dir: filepath.Join(home, ".codex", "sessions")}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.dir); err != nil {
		return false, "", nil
	}
	return true, s.dir, nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return nil, err
	}
	var refs []ports.SessionRef
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			continue
		}
		if !since.IsZero() && st.ModTime().Before(since) {
			continue
		}
		refs = append(refs, ports.SessionRef{
			Agent:     agentName,
			SourceID:  sourceID(f),
			Files:     []string{f},
			UpdatedAt: st.ModTime().UTC(),
		})
	}
	return refs, nil
}

// sourceID extracts the session uuid from
// "rollout-2026-06-07T14-04-59-019ea0af-3d6a-7393-8ccf-a4ae49f116c3.jsonl":
// the uuid is the trailing 36 characters. session_meta later overrides this
// with the authoritative id.
func sourceID(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	name = strings.TrimPrefix(name, "rollout-")
	if len(name) >= 36 {
		return name[len(name)-36:]
	}
	return name
}

type line struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	CLIVersion    string `json:"cli_version"`
	ModelProvider string `json:"model_provider"`
	Git           struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type responseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`      // function_call
	Arguments string          `json:"arguments"` // function_call
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"` // function_call_output
}

type turnContext struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
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
	// function_call has no stable parent message; collect calls and attach
	// the matching output by call_id.
	callByID := map[string]*model.ToolCall{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		switch l.Type {
		case "session_meta":
			var m sessionMeta
			if json.Unmarshal(l.Payload, &m) == nil {
				if m.ID != "" {
					sess.SourceID = m.ID
					sess.ID = agentName + "/" + m.ID
				}
				sess.CWD = m.CWD
				sess.GitBranch = m.Git.Branch
				if m.CLIVersion != "" {
					sess.Meta["cli_version"] = m.CLIVersion
				}
				if m.ModelProvider != "" {
					sess.Meta["model_provider"] = m.ModelProvider
				}
				sess.StartedAt = l.Timestamp.UTC()
			}
		case "turn_context":
			var tc turnContext
			if json.Unmarshal(l.Payload, &tc) == nil && tc.Model != "" {
				sess.Model = tc.Model
			}
		case "response_item":
			var item responseItem
			if json.Unmarshal(l.Payload, &item) != nil {
				continue
			}
			switch item.Type {
			case "message":
				text := contentText(item.Content)
				if text == "" {
					continue
				}
				sess.Messages = append(sess.Messages, model.Message{
					Role:      normalizeRole(item.Role),
					Text:      text,
					Timestamp: l.Timestamp.UTC(),
					Raw:       json.RawMessage(append([]byte(nil), raw...)),
				})
			case "function_call", "custom_tool_call":
				tc := model.ToolCall{
					Name:  item.Name,
					Input: scan.Truncate(item.Arguments, scan.MaxToolIO),
				}
				msg := model.Message{
					Role:      model.RoleAssistant,
					ToolCalls: []model.ToolCall{tc},
					Timestamp: l.Timestamp.UTC(),
					Raw:       json.RawMessage(append([]byte(nil), raw...)),
				}
				sess.Messages = append(sess.Messages, msg)
				if item.CallID != "" {
					callByID[item.CallID] = &sess.Messages[len(sess.Messages)-1].ToolCalls[0]
				}
			case "function_call_output", "custom_tool_call_output":
				out := outputText(item.Output)
				if tc, ok := callByID[item.CallID]; ok {
					tc.Output = scan.Truncate(out, scan.MaxToolIO)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(sess.Messages) == 0 {
		return nil, nil
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = sess.Messages[0].Timestamp
	}
	sess.UpdatedAt = sess.Messages[len(sess.Messages)-1].Timestamp
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = ref.UpdatedAt
	}
	sess.Title = scan.FirstLine(firstUserText(sess), 80)
	return sess, nil
}

func normalizeRole(r string) model.Role {
	switch r {
	case "user":
		return model.RoleUser
	case "assistant":
		return model.RoleAssistant
	case "system", "developer":
		return model.RoleSystem
	default:
		return model.Role(r)
	}
}

// contentText joins input_text/output_text blocks; environment context
// blocks injected by codex are excluded from user text.
func contentText(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Text == "" || strings.HasPrefix(b.Text, "<environment_context>") {
			continue
		}
		texts = append(texts, b.Text)
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

// outputText handles function_call_output payloads, which are either a
// plain string or {"output": "...", ...} / {"content": "..."} objects.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Output  string `json:"output"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Output != "" {
			return obj.Output
		}
		return obj.Content
	}
	return ""
}

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}
