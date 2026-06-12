package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// seedConv writes one conversation's transcript_full.jsonl mirroring the
// real Antigravity step shape (sources/types from the published docs).
func seedConv(t *testing.T, home, convID string) string {
	t.Helper()
	logs := filepath.Join(home, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, "transcript_full.jsonl")
	lines := []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-24T12:14:37Z","content":"What is Google Cloud Run?"}`,
		`{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE","created_at":"2026-05-24T12:14:37Z","content":"<resume payload>"}`,
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-24T12:14:38Z","thinking":"Let me search the docs","tool_calls":[{"name":"search_web","args":{"query":"cloud run overview"}}]}`,
		`{"step_index":3,"source":"MODEL","type":"SEARCH_WEB","status":"DONE","created_at":"2026-05-24T12:14:40Z","content":"Cloud Run is a managed compute platform..."}`,
		`{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-24T12:14:46Z","content":"**Cloud Run** is serverless compute by Google Cloud."}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectAndList(t *testing.T) {
	home := t.TempDir()
	src := New(home)
	if found, _, _ := src.Detect(context.Background()); found {
		t.Fatal("Detect on empty home should be false")
	}
	seedConv(t, home, "11111111-2222-3333-4444-555555555555")
	if found, root, _ := src.Detect(context.Background()); !found ||
		root != filepath.Join(home, ".gemini", "antigravity-cli") {
		t.Fatalf("Detect after seed: found=%v root=%s", found, root)
	}
	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].SourceID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestListPrefersFullTranscript(t *testing.T) {
	home := t.TempDir()
	logs := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "abc", ".system_generated", "logs")
	_ = os.MkdirAll(logs, 0o700)
	_ = os.WriteFile(filepath.Join(logs, "transcript.jsonl"), []byte("{}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), []byte("{}\n"), 0o600)

	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})
	if len(refs) != 1 {
		t.Fatalf("got %d refs", len(refs))
	}
	if base := filepath.Base(refs[0].Files[0]); base != "transcript_full.jsonl" {
		t.Errorf("expected full transcript, got %s", base)
	}
}

func TestLoadShapesMessages(t *testing.T) {
	home := t.TempDir()
	seedConv(t, home, "abc")
	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})

	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("session is nil")
	}
	// CONVERSATION_HISTORY (step 1) is dropped from the dialogue, so we
	// expect 4 visible messages: user input, planner+tool_call,
	// SEARCH_WEB tool result, final planner response.
	if got := len(s.Messages); got != 4 {
		t.Fatalf("messages: got %d, want 4 (CONVERSATION_HISTORY must be dropped)", got)
	}
	if s.Messages[0].Role != model.RoleUser ||
		!strings.Contains(s.Messages[0].Text, "Cloud Run") {
		t.Errorf("user message: %+v", s.Messages[0])
	}
	planner := s.Messages[1]
	if planner.Role != model.RoleAssistant {
		t.Errorf("planner role: %v", planner.Role)
	}
	if !strings.Contains(planner.Text, "[thinking]") {
		t.Errorf("planner thinking not tagged: %q", planner.Text)
	}
	if len(planner.ToolCalls) != 1 || planner.ToolCalls[0].Name != "search_web" {
		t.Errorf("planner tool call: %+v", planner.ToolCalls)
	}
	// SEARCH_WEB step is a tool result emitted by MODEL — must surface
	// as RoleTool, not RoleAssistant.
	if s.Messages[2].Role != model.RoleTool {
		t.Errorf("SEARCH_WEB step should be RoleTool, got %v", s.Messages[2].Role)
	}
	// Title comes from the first user prompt.
	if !strings.Contains(s.Title, "Cloud Run") {
		t.Errorf("title: %q", s.Title)
	}
}

func TestLoadFailSoftOnGarbage(t *testing.T) {
	home := t.TempDir()
	logs := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "x", ".system_generated", "logs")
	_ = os.MkdirAll(logs, 0o700)
	body := `not-json
{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-24T12:14:37Z","content":"hello"}
{"step_index":99,"source":"FUTURE_SOURCE","type":"FUTURE_TYPE","status":"DONE"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-24T12:14:38Z","content":"hi"}
`
	_ = os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), []byte(body), 0o600)
	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})
	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 surviving messages, got %d", len(s.Messages))
	}
}
