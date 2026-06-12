package gemini

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// seedSession writes a synthetic but schema-accurate Gemini CLI session
// file matching the formats produced by gemini-cli's ChatRecordingService.
func seedSession(t *testing.T, dir, projectHash string) string {
	t.Helper()
	chats := filepath.Join(dir, ".gemini", "tmp", projectHash, "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chats, "session-2026-06-12T10-00-00-d7501ec6.jsonl")
	// Multi-line composition that mirrors a real chat:
	// 1) leading metadata, 2) user with string content, 3) gemini with
	// Part[] content + a tool call, 4) a $set patch for the title,
	// 5) a $rewindTo that drops the last record, 6) a final assistant turn.
	lines := []string{
		`{"sessionId":"d7501ec6","projectHash":"abc123","startTime":"2026-06-12T10:00:00.000Z","kind":"interactive","directories":["/root/code/demo"]}`,
		`{"id":"m1","type":"user","content":"为什么 build 越来越慢","timestamp":"2026-06-12T10:00:01.000Z"}`,
		`{"id":"m2","type":"gemini","content":[{"text":"先量测下："}],"model":"gemini-2.5-pro","timestamp":"2026-06-12T10:00:02.000Z","toolCalls":[{"id":"t1","name":"shell","args":{"cmd":"npm run build --profile"},"result":{"stdout":"webpack 23s"},"status":"success"}]}`,
		`{"$set":{"summary":"Diagnose slow build"}}`,
		`{"id":"m3","type":"gemini","content":"先撤回这一步看下日志","timestamp":"2026-06-12T10:00:03.000Z"}`,
		`{"$rewindTo":"m3"}`,
		`{"id":"m4","type":"gemini","content":"webpack profile 显示 ts-loader 占 90%","timestamp":"2026-06-12T10:00:04.000Z"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectAndList(t *testing.T) {
	dir := t.TempDir()
	src := New(dir)

	// Empty home: nothing detected.
	if found, _, _ := src.Detect(context.Background()); found {
		t.Fatal("Detect returned true on empty home")
	}
	if refs, _ := src.List(context.Background(), zeroTime()); len(refs) != 0 {
		t.Fatalf("List on empty home: got %d", len(refs))
	}

	seedSession(t, dir, "abc123")
	if found, root, _ := src.Detect(context.Background()); !found || root != filepath.Join(dir, ".gemini") {
		t.Fatalf("Detect after seed: found=%v root=%s", found, root)
	}
	refs, err := src.List(context.Background(), zeroTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("List: got %d refs, want 1", len(refs))
	}
	if refs[0].Agent != agentName {
		t.Errorf("agent: %s", refs[0].Agent)
	}
	if refs[0].SourceID != "d7501ec6" {
		t.Errorf("sourceID: %s want d7501ec6", refs[0].SourceID)
	}
}

func TestLoadParsesMixedContent(t *testing.T) {
	dir := t.TempDir()
	seedSession(t, dir, "abc123")
	src := New(dir)
	refs, _ := src.List(context.Background(), zeroTime())

	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("session is nil")
	}
	if s.Title != "Diagnose slow build" {
		t.Errorf("title from $set: %q", s.Title)
	}
	if s.CWD != "/root/code/demo" {
		t.Errorf("cwd from directories: %q", s.CWD)
	}
	if s.Model != "gemini-2.5-pro" {
		t.Errorf("model: %q", s.Model)
	}
	// 4 messages were written; $rewindTo m3 drops m3, leaving m1, m2, m4.
	if len(s.Messages) != 3 {
		t.Fatalf("messages after rewind: got %d, want 3", len(s.Messages))
	}
	if s.Messages[0].Role != model.RoleUser || s.Messages[0].Text != "为什么 build 越来越慢" {
		t.Errorf("user message: %+v", s.Messages[0])
	}
	if s.Messages[1].Role != model.RoleAssistant {
		t.Errorf("assistant role: %v", s.Messages[1].Role)
	}
	if len(s.Messages[1].ToolCalls) != 1 || s.Messages[1].ToolCalls[0].Name != "shell" {
		t.Errorf("tool call: %+v", s.Messages[1].ToolCalls)
	}
	if !strings.Contains(s.Messages[1].ToolCalls[0].Output, "webpack 23s") {
		t.Errorf("tool output not captured: %q", s.Messages[1].ToolCalls[0].Output)
	}
	if s.Messages[2].ID != "m4" {
		t.Errorf("expected m4 as last message after rewind, got %s", s.Messages[2].ID)
	}
}

func TestLoadFailSoftOnUnknownRecords(t *testing.T) {
	dir := t.TempDir()
	chats := filepath.Join(dir, ".gemini", "tmp", "abc", "chats")
	_ = os.MkdirAll(chats, 0o700)
	p := filepath.Join(chats, "session-2026-x-aaaaaaaa.jsonl")
	lines := []string{
		`{"sessionId":"aaaaaaaa"}`,
		`not-json-at-all`,
		`{"id":"m1","type":"user","content":"hi","timestamp":"2026-06-12T10:00:01.000Z"}`,
		`{"id":"m2","type":"weird-future-type","content":"???"}`,
		`{"id":"m3","type":"gemini","content":"hello","timestamp":"2026-06-12T10:00:02.000Z"}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	src := New(dir)
	refs, _ := src.List(context.Background(), zeroTime())
	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || len(s.Messages) != 2 {
		t.Fatalf("expected 2 surviving messages, got %v", s)
	}
}

func TestRawIsPreserved(t *testing.T) {
	dir := t.TempDir()
	seedSession(t, dir, "abc123")
	src := New(dir)
	refs, _ := src.List(context.Background(), zeroTime())
	s, _ := src.Load(context.Background(), refs[0])

	// Round-trip the raw JSON of the first message — parser must keep
	// the original bytes for the archive layer.
	var got map[string]any
	if err := json.Unmarshal(s.Messages[0].Raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "m1" || got["type"] != "user" {
		t.Errorf("raw fields lost: %v", got)
	}
}

func zeroTime() (t time.Time) { return }
