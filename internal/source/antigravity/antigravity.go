// Package antigravity reads Google Antigravity CLI sessions from
// ~/.gemini/antigravity-cli/brain/<conversation-id>/.system_generated/logs/transcript_full.jsonl.
//
// Antigravity is Google's closed-source successor to Gemini CLI
// (announced at I/O 2026; free/Pro/Ultra access to Gemini CLI sunsets
// 2026-06-18). It reuses the ~/.gemini/ prefix but uses a brand-new
// JSONL transcript schema, so it warrants its own parser rather than
// piggybacking on the gemini package.
//
// Each line is a "step" record:
//
//	{
//	  "step_index": 0,
//	  "source": "USER_EXPLICIT" | "MODEL" | "SYSTEM" | ...,
//	  "type":   "USER_INPUT" | "PLANNER_RESPONSE" | "SEARCH_WEB" |
//	            "CONVERSATION_HISTORY" | "TOOL_RESULT" | ...,
//	  "status": "DONE" | "RUNNING" | ...,
//	  "created_at": "2026-05-24T12:14:37Z",
//	  "content":  "rendered text",
//	  "thinking": "model reasoning",
//	  "tool_calls": [ {"name", "args", "toolAction", "toolSummary"} ]
//	}
//
// We use `transcript_full.jsonl` (untruncated) when present, falling back
// to the older `transcript.jsonl` when it isn't. Sibling artifacts —
// implementation_plan.md, task.md, walkthrough.md — stay in the raw
// archive but aren't surfaced as messages because they are agent-managed
// scratchpads rather than dialogue.
package antigravity

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

const agentName = "antigravity"

type Source struct {
	root string // ~/.gemini/antigravity-cli
}

func New(home string) *Source {
	return &Source{root: filepath.Join(home, ".gemini", "antigravity-cli")}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(filepath.Join(s.root, "brain")); err != nil {
		return false, "", nil
	}
	return true, s.root, nil
}

// List walks brain/<uuid>/.system_generated/logs/ and registers one ref
// per conversation. We prefer transcript_full.jsonl (verbatim) over
// transcript.jsonl (truncated) when both exist.
func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	brain := filepath.Join(s.root, "brain")
	entries, err := os.ReadDir(brain)
	if err != nil {
		return nil, nil // empty / missing is not an error
	}
	var refs []ports.SessionRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		convID := e.Name()
		logsDir := filepath.Join(brain, convID, ".system_generated", "logs")
		path := filepath.Join(logsDir, "transcript_full.jsonl")
		st, err := os.Stat(path)
		if err != nil || st.Size() == 0 {
			alt := filepath.Join(logsDir, "transcript.jsonl")
			st, err = os.Stat(alt)
			if err != nil || st.Size() == 0 {
				continue
			}
			path = alt
		}
		if !since.IsZero() && st.ModTime().Before(since) {
			continue
		}
		refs = append(refs, ports.SessionRef{
			Agent:     agentName,
			SourceID:  convID,
			Files:     []string{path},
			UpdatedAt: st.ModTime().UTC(),
		})
	}
	return refs, nil
}

// step matches the JSONL record described in the package doc. We keep
// only the fields we render; unknown ones survive in raw bytes.
type step struct {
	StepIndex int        `json:"step_index"`
	Source    string     `json:"source"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	CreatedAt string     `json:"created_at"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking"`
	ToolCalls []toolCall `json:"tool_calls"`
}

type toolCall struct {
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args"`
	ToolAction  string          `json:"toolAction"`
	ToolSummary string          `json:"toolSummary"`
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
		Meta: map[string]string{
			"transcript": filepath.Base(ref.Files[0]),
		},
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // long tool outputs

	for sc.Scan() {
		raw := sc.Bytes()
		var st step
		if err := json.Unmarshal(raw, &st); err != nil {
			continue
		}
		// CONVERSATION_HISTORY records are bookkeeping summaries that
		// Antigravity injects on resume — they'd double-count the
		// dialogue if we treated them as messages. Skip; raw still has them.
		if st.Type == "CONVERSATION_HISTORY" {
			continue
		}
		msg, ok := convertStep(st, raw)
		if !ok {
			continue
		}
		sess.Messages = append(sess.Messages, msg)
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

func convertStep(s step, raw []byte) (model.Message, bool) {
	role := normalizeRole(s.Source, s.Type)
	if role == "" {
		return model.Message{}, false
	}
	ts, _ := time.Parse(time.RFC3339, s.CreatedAt)

	// Body: thinking first (clearly tagged), then content. Search needs
	// reasoning, replay benefits from seeing the agent's chain of thought.
	var body []string
	if s.Thinking != "" {
		body = append(body, "[thinking] "+s.Thinking)
	}
	if s.Content != "" {
		body = append(body, s.Content)
	}

	msg := model.Message{
		ID:        idForStep(s),
		Role:      role,
		Text:      strings.TrimSpace(strings.Join(body, "\n")),
		Timestamp: ts.UTC(),
		Raw:       json.RawMessage(append([]byte(nil), raw...)),
	}
	for _, tc := range s.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
			Name:  tc.Name,
			Input: scan.Truncate(jsonText(tc.Args), scan.MaxToolIO),
		})
	}
	if msg.Text == "" && len(msg.ToolCalls) == 0 {
		return model.Message{}, false
	}
	return msg, true
}

// normalizeRole maps Antigravity's (source, type) pair onto tape's Role.
// `source` is the actor; `type` distinguishes user prompts from tool
// results (which are technically MODEL-sourced but should be RoleTool).
func normalizeRole(source, typ string) model.Role {
	src := strings.ToUpper(source)
	tp := strings.ToUpper(typ)
	switch src {
	case "USER_EXPLICIT", "USER", "USER_IMPLICIT":
		return model.RoleUser
	case "MODEL":
		// Tool-shaped record types are the model's tool outputs —
		// surface them as RoleTool so replay logic groups them with
		// the call that produced them.
		switch tp {
		case "SEARCH_WEB", "TOOL_RESULT", "READ_FILE", "WRITE_FILE",
			"RUN_COMMAND", "BROWSER", "EDIT_FILE", "GREP", "GLOB":
			return model.RoleTool
		}
		return model.RoleAssistant
	case "SYSTEM":
		// SYSTEM-source records (e.g. summarization, init) — skip from
		// the dialogue replay; raw line still archived.
		return ""
	default:
		return ""
	}
}

// idForStep gives every step a stable, short id. Antigravity doesn't put
// uuids on individual steps, so we synthesize one from (step_index, ts).
func idForStep(s step) string {
	return "step-" + intToStr(s.StepIndex)
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
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
