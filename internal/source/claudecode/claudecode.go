// Package claudecode reads Claude Code sessions from
// ~/.claude/projects/<project-slug>/<session-uuid>.jsonl.
//
// Each line is a typed JSON record. Message lines (type user/assistant)
// carry an Anthropic-style message plus envelope metadata (uuid, parentUuid,
// cwd, gitBranch, ...). Other line types (mode, attachment, ai-title,
// file-history-snapshot, ...) are metadata; unknown types are ignored
// (fail-soft) and remain available in the raw archive copy.
package claudecode

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

const agentName = "claude-code"

type Source struct {
	dir string // ~/.claude/projects
}

func New(home string) *Source {
	return &Source{dir: filepath.Join(home, ".claude", "projects")}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.dir); err != nil {
		return false, "", nil
	}
	return true, s.dir, nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*", "*.jsonl"))
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
			SourceID:  strings.TrimSuffix(filepath.Base(f), ".jsonl"),
			Files:     []string{f},
			UpdatedAt: st.ModTime().UTC(),
		})
	}
	return refs, nil
}

// line is the superset of fields we care about across line types.
type line struct {
	Type       string          `json:"type"`
	UUID       string          `json:"uuid"`
	ParentUUID string          `json:"parentUuid"`
	Timestamp  time.Time       `json:"timestamp"`
	CWD        string          `json:"cwd"`
	GitBranch  string          `json:"gitBranch"`
	Version    string          `json:"version"`
	AITitle    string          `json:"aiTitle"`
	Summary    string          `json:"summary"`
	IsSidechain bool           `json:"isSidechain"`
	Message    json.RawMessage `json:"message"`
}

type apiMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`            // tool_use
	Input   json.RawMessage `json:"input"`           // tool_use
	Content json.RawMessage `json:"content"`         // tool_result
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
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // tool results can be huge
	for sc.Scan() {
		raw := sc.Bytes()
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue // fail-soft: skip unparseable lines, raw copy keeps them
		}
		switch l.Type {
		case "ai-title":
			sess.Title = l.AITitle
		case "summary":
			if sess.Title == "" {
				sess.Title = l.Summary
			}
		case "user", "assistant":
			if l.IsSidechain {
				continue // sub-agent traffic; keep main thread only
			}
			msg, ok := parseMessage(l, raw)
			if !ok {
				continue
			}
			sess.Messages = append(sess.Messages, msg)
			if l.CWD != "" {
				sess.CWD = l.CWD
			}
			if l.GitBranch != "" {
				sess.GitBranch = l.GitBranch
			}
			if l.Version != "" {
				sess.Meta["cli_version"] = l.Version
			}
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

func parseMessage(l line, raw []byte) (model.Message, bool) {
	var am apiMessage
	if err := json.Unmarshal(l.Message, &am); err != nil {
		return model.Message{}, false
	}
	msg := model.Message{
		ID:        l.UUID,
		ParentID:  l.ParentUUID,
		Role:      model.Role(am.Role),
		Timestamp: l.Timestamp.UTC(),
		Raw:       json.RawMessage(append([]byte(nil), raw...)),
	}

	// content is either a plain string or a list of typed blocks
	var text string
	if err := json.Unmarshal(am.Content, &text); err == nil {
		msg.Text = text
	} else {
		var blocks []contentBlock
		if err := json.Unmarshal(am.Content, &blocks); err != nil {
			return model.Message{}, false
		}
		var texts []string
		toolResults := 0
		for _, b := range blocks {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
					Name:  b.Name,
					Input: scan.Truncate(string(b.Input), scan.MaxToolIO),
				})
			case "tool_result":
				toolResults++
				texts = append(texts, scan.Truncate(blockText(b.Content), scan.MaxToolIO))
			}
		}
		msg.Text = strings.TrimSpace(strings.Join(texts, "\n"))
		// a "user" line whose content is only tool_result blocks is really
		// tool output, not something the human typed
		if am.Role == "user" && toolResults > 0 && toolResults == len(blocks) {
			msg.Role = model.RoleTool
		}
	}
	if msg.Text == "" && len(msg.ToolCalls) == 0 {
		return model.Message{}, false
	}
	return msg, true
}

// blockText extracts text from a tool_result content, which is either a
// string or a list of {type:"text",text:...} blocks.
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
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
