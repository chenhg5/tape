// Package model defines tape's provider-agnostic session model (the IR).
// Every agent's native format is normalized into these types; everything
// downstream (archive, index, search, restore) depends only on them.
package model

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// Session is a normalized agent conversation.
type Session struct {
	// ID is tape's global identifier: "<agent>/<source-id>".
	ID        string            `json:"id"`
	Agent     string            `json:"agent"`
	SourceID  string            `json:"source_id"`
	Title     string            `json:"title,omitempty"`
	CWD       string            `json:"cwd,omitempty"`
	GitBranch string            `json:"git_branch,omitempty"`
	Model     string            `json:"model,omitempty"`
	StartedAt time.Time         `json:"started_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  []Message         `json:"messages"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type Message struct {
	ID        string     `json:"id,omitempty"`
	ParentID  string     `json:"parent_id,omitempty"`
	Role      Role       `json:"role"`
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Timestamp time.Time  `json:"timestamp,omitempty"`
	// Raw preserves the original provider record verbatim so the IR is
	// lossless even when normalization doesn't understand a field.
	Raw json.RawMessage `json:"raw,omitempty"`
}

type ToolCall struct {
	Name   string `json:"name"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

// Summary is the lightweight listing view of a session, cheap to load
// without parsing the full message list.
type Summary struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Title     string    `json:"title,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	Project   string    `json:"project"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	MsgCount  int       `json:"msg_count"`
}

func (s *Session) Summary() Summary {
	return Summary{
		ID:        s.ID,
		Agent:     s.Agent,
		Title:     s.Title,
		CWD:       s.CWD,
		Project:   ProjectSlug(s.CWD),
		StartedAt: s.StartedAt,
		UpdatedAt: s.UpdatedAt,
		MsgCount:  len(s.Messages),
	}
}
