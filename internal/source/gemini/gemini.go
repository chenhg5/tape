// Package gemini reads Google Gemini CLI sessions from
// ~/.gemini/tmp/<project-hash>/chats/session-*.jsonl.
//
// Schema (per gemini-cli's ChatRecordingService):
//
//   - First line is a metadata object: {"sessionId":..., "projectHash":...,
//     "startTime":..., "kind":..., "directories": [...], ...}.
//   - Subsequent lines are MessageRecord objects keyed by `id`:
//
//     {"id":"m1","type":"user"|"gemini",
//      "content": <string | PartListUnion>,   // gemini Part = {text}|{functionCall}|{functionResponse}|{thought}
//      "displayContent": <PartListUnion>,
//      "model":"gemini-2.5-pro",
//      "toolCalls":[{"id","name","args","result","status","displayName",...}],
//      "thoughts":[{"subject","description"}],
//      "tokens":{"input","output","total","cached","thoughts","tool"} }
//
//   - Metadata patches look like {"$set":{"summary":"..."}}.
//   - Rewind markers look like {"$rewindTo":"<message-id>"} and truncate
//     history up to that record. We honor them on read.
//
// Unknown record shapes are skipped (fail-soft): the raw file is always
// preserved by tape's archive layer, so nothing is lost on disk.
package gemini

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

const agentName = "gemini"

// Source reads Gemini CLI conversations from $HOME/.gemini.
type Source struct {
	root string // ~/.gemini
}

func New(home string) *Source {
	return &Source{root: filepath.Join(home, ".gemini")}
}

func (s *Source) Name() string { return agentName }

// Detect reports whether the Gemini directory layout is present. We accept
// either the canonical `~/.gemini/tmp/<hash>/chats/` (active sessions) or
// `~/.gemini/sessions/` (export target seen in older docs) — being lenient
// here keeps us from missing data when Google reshuffles paths.
func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.root); err != nil {
		return false, "", nil
	}
	for _, sub := range []string{"tmp", "sessions"} {
		if _, err := os.Stat(filepath.Join(s.root, sub)); err == nil {
			return true, s.root, nil
		}
	}
	return false, "", nil
}

// List walks every chats/session-*.jsonl under tmp/ and sessions/. Each
// file is one session. Empty files are skipped so we don't fingerprint
// freshly-created sessions before the first user message lands.
func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	var refs []ports.SessionRef
	patterns := []string{
		filepath.Join(s.root, "tmp", "*", "chats", "session-*.jsonl"),
		filepath.Join(s.root, "tmp", "*", "chats", "session-*.json"),
		filepath.Join(s.root, "sessions", "*.jsonl"),
		filepath.Join(s.root, "sessions", "*.json"),
	}
	seen := map[string]bool{}
	for _, p := range patterns {
		files, err := filepath.Glob(p)
		if err != nil {
			continue
		}
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
				SourceID:  sessionIDFromFilename(f),
				Files:     []string{f},
				UpdatedAt: st.ModTime().UTC(),
			})
		}
	}
	return refs, nil
}

// sessionIDFromFilename pulls the trailing 8-hex slug out of a filename
// like `session-2026-03-04T12-42-295d2fe2.jsonl`. Falls back to the bare
// stem when the pattern doesn't match — better than nothing, and Resolve
// only needs uniqueness within an agent.
func sessionIDFromFilename(f string) string {
	stem := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".jsonl"), ".json")
	if i := strings.LastIndex(stem, "-"); i >= 0 && i+1 < len(stem) {
		tail := stem[i+1:]
		if len(tail) >= 6 { // looks like the random suffix gemini appends
			return tail
		}
	}
	return strings.TrimPrefix(stem, "session-")
}

// metaLine matches both the leading metadata object and any $set patches.
type metaLine struct {
	SessionID   string   `json:"sessionId"`
	ProjectHash string   `json:"projectHash"`
	StartTime   string   `json:"startTime"`
	Kind        string   `json:"kind"`
	Directories []string `json:"directories"`
	Summary     string   `json:"summary"`
	Set         *metaSet `json:"$set"`
	RewindTo    string   `json:"$rewindTo"`
}

type metaSet struct {
	Summary     string   `json:"summary"`
	Directories []string `json:"directories"`
}

// msgLine carries a single MessageRecord plus the rewind/patch envelope.
type msgLine struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Content        json.RawMessage `json:"content"`
	DisplayContent json.RawMessage `json:"displayContent"`
	Model          string          `json:"model"`
	Timestamp      string          `json:"timestamp"`
	ToolCalls      []toolCall      `json:"toolCalls"`
	Thoughts       []thought       `json:"thoughts"`
}

type toolCall struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Args        json.RawMessage `json:"args"`
	Result      json.RawMessage `json:"result"`
	Status      string          `json:"status"`
}

type thought struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}

// part is one element of Gemini's PartListUnion. Each Part carries exactly
// one of {text, functionCall, functionResponse, thought, ...}; we keep
// the fields we render and let json drop the rest.
type part struct {
	Text             string          `json:"text"`
	Thought          json.RawMessage `json:"thought"`
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
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // tool results can be large

	type rec struct {
		msg model.Message
	}
	var records []rec

	for sc.Scan() {
		raw := sc.Bytes()
		// Try a metadata / patch line first — these have no `id` but may
		// carry $set / $rewindTo / sessionId.
		var ml metaLine
		if err := json.Unmarshal(raw, &ml); err == nil {
			if ml.SessionID != "" {
				sess.Meta["session_id"] = ml.SessionID
			}
			if ml.ProjectHash != "" {
				sess.Meta["project_hash"] = ml.ProjectHash
				if sess.CWD == "" && len(ml.Directories) > 0 {
					sess.CWD = ml.Directories[0]
				}
			}
			if ml.StartTime != "" && sess.StartedAt.IsZero() {
				if t, err := time.Parse(time.RFC3339, ml.StartTime); err == nil {
					sess.StartedAt = t.UTC()
				}
			}
			if ml.Summary != "" && sess.Title == "" {
				sess.Title = ml.Summary
			}
			if ml.Set != nil {
				if ml.Set.Summary != "" {
					sess.Title = ml.Set.Summary
				}
				if sess.CWD == "" && len(ml.Set.Directories) > 0 {
					sess.CWD = ml.Set.Directories[0]
				}
			}
			if ml.RewindTo != "" {
				// Truncate the message stream at (and including) the
				// rewind target — this matches how gemini-cli reloads
				// after the user rewinds a turn.
				for i, r := range records {
					if r.msg.ID == ml.RewindTo {
						records = records[:i]
						break
					}
				}
			}
		}

		// Then try to read it as a MessageRecord.
		var line msgLine
		if err := json.Unmarshal(raw, &line); err != nil || line.ID == "" {
			continue
		}
		m, ok := convertMessage(line, raw)
		if !ok {
			continue
		}
		records = append(records, rec{msg: m})
		if line.Model != "" {
			sess.Model = line.Model
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	for _, r := range records {
		sess.Messages = append(sess.Messages, r.msg)
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
	if sess.Title == "" {
		sess.Title = scan.FirstLine(firstUserText(sess), 80)
	}
	return sess, nil
}

func convertMessage(line msgLine, raw []byte) (model.Message, bool) {
	role := normalizeRole(line.Type)
	if role == "" {
		return model.Message{}, false
	}
	ts, _ := time.Parse(time.RFC3339, line.Timestamp)
	m := model.Message{
		ID:        line.ID,
		Role:      role,
		Timestamp: ts.UTC(),
		Raw:       json.RawMessage(append([]byte(nil), raw...)),
	}

	// content may be a plain string, a Part[], or absent (tool-only turn).
	m.Text = strings.TrimSpace(extractText(line.Content))
	if m.Text == "" {
		m.Text = strings.TrimSpace(extractText(line.DisplayContent))
	}

	for _, tc := range line.ToolCalls {
		name := tc.Name
		if name == "" {
			name = tc.DisplayName
		}
		m.ToolCalls = append(m.ToolCalls, model.ToolCall{
			Name:   name,
			Input:  scan.Truncate(jsonText(tc.Args), scan.MaxToolIO),
			Output: scan.Truncate(jsonText(tc.Result), scan.MaxToolIO),
		})
	}

	// Pure "thought" turns with no surfaced content or tool calls aren't
	// useful in the dialogue replay — they'd just be empty bullets.
	if m.Text == "" && len(m.ToolCalls) == 0 {
		return model.Message{}, false
	}
	return m, true
}

func normalizeRole(t string) model.Role {
	switch strings.ToLower(t) {
	case "user":
		return model.RoleUser
	case "gemini", "assistant", "model":
		return model.RoleAssistant
	case "tool", "tool_result", "function":
		return model.RoleTool
	default:
		return ""
	}
}

// extractText flattens a PartListUnion to plain text. Strings come through
// verbatim; Part arrays get joined on newline; anything else becomes "".
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
			continue
		}
		if p.FunctionCall != nil {
			out = append(out, "[call "+p.FunctionCall.Name+"]")
		}
		if p.FunctionResponse != nil {
			out = append(out, "[result "+p.FunctionResponse.Name+"]")
		}
	}
	return strings.Join(out, "\n")
}

// jsonText renders a json.RawMessage to a readable string: prefer the
// raw text for objects/arrays, unquote plain strings.
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
