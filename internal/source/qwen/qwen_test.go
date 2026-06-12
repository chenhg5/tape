package qwen

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

// seedSession composes a Qwen ChatRecord stream that exercises every code
// path we care about: a user/assistant exchange, a function call, a
// tool_result echo, and a system event that must be skipped.
func seedSession(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".qwen", "projects", "abc123", "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sid := "session-001"
	lines := []string{
		`{"uuid":"u1","parentUuid":null,"sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:00.000Z","type":"user","cwd":"/root/code/demo","version":"0.10.0","gitBranch":"main","message":{"role":"user","parts":[{"text":"实现 jwt 鉴权"}]}}`,
		`{"uuid":"a1","parentUuid":"u1","sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:01.000Z","type":"assistant","cwd":"/root/code/demo","model":"qwen2.5-coder-30b","message":{"role":"model","parts":[{"text":"先看现有 auth 中间件："},{"functionCall":{"name":"read_file","args":{"path":"auth.go"}}}]}}`,
		`{"uuid":"t1","parentUuid":"a1","sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:02.000Z","type":"tool_result","cwd":"/root/code/demo","toolCallResult":{"name":"read_file","callId":"t1","args":{"path":"auth.go"},"result":"package main\n// existing middleware","status":"success"}}`,
		`{"uuid":"s1","parentUuid":"t1","sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:03.000Z","type":"system","subtype":"chat_compression"}`,
		`{"uuid":"a2","parentUuid":"t1","sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:04.000Z","type":"assistant","cwd":"/root/code/demo","message":{"role":"model","parts":[{"text":"已读取，准备替换为 jwt"}]}}`,
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectAndList(t *testing.T) {
	dir := t.TempDir()
	src := New(dir)

	if found, _, _ := src.Detect(context.Background()); found {
		t.Fatal("Detect on empty home should be false")
	}
	seedSession(t, dir)
	if found, _, _ := src.Detect(context.Background()); !found {
		t.Fatal("Detect after seed should be true")
	}
	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].SourceID != "session-001" {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestLoadShapesMessages(t *testing.T) {
	dir := t.TempDir()
	seedSession(t, dir)
	src := New(dir)
	refs, _ := src.List(context.Background(), time.Time{})

	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("session is nil")
	}
	if s.CWD != "/root/code/demo" || s.GitBranch != "main" {
		t.Errorf("metadata not captured: cwd=%q branch=%q", s.CWD, s.GitBranch)
	}
	if s.Model != "qwen2.5-coder-30b" {
		t.Errorf("model: %q", s.Model)
	}
	// system row dropped, so 4 visible messages
	if len(s.Messages) != 4 {
		t.Fatalf("messages: got %d want 4 (system row must be dropped)", len(s.Messages))
	}
	if s.Messages[0].Role != model.RoleUser || s.Messages[0].Text != "实现 jwt 鉴权" {
		t.Errorf("user message: %+v", s.Messages[0])
	}
	asst := s.Messages[1]
	if asst.Role != model.RoleAssistant || asst.Text == "" || len(asst.ToolCalls) != 1 {
		t.Errorf("assistant w/ function call: %+v", asst)
	}
	if asst.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call name: %q", asst.ToolCalls[0].Name)
	}
	tr := s.Messages[2]
	if tr.Role != model.RoleTool {
		t.Errorf("tool_result must map to RoleTool, got %v", tr.Role)
	}
	if !strings.Contains(tr.Text, "existing middleware") {
		t.Errorf("tool result text not captured: %q", tr.Text)
	}
	// Tool result must also expose Output on its synthetic tool call so
	// the FTS indexer can find it.
	if len(tr.ToolCalls) != 1 || !strings.Contains(tr.ToolCalls[0].Output, "existing middleware") {
		t.Errorf("tool result output not attached: %+v", tr.ToolCalls)
	}
}

func TestLoadFailSoftOnGarbage(t *testing.T) {
	dir := t.TempDir()
	chats := filepath.Join(dir, ".qwen", "projects", "x", "chats")
	_ = os.MkdirAll(chats, 0o700)
	p := filepath.Join(chats, "s.jsonl")
	lines := []string{
		`not-json`,
		`{"uuid":"u1","sessionId":"s","timestamp":"2026-06-12T10:00:00.000Z","type":"user","message":{"role":"user","parts":[{"text":"hi"}]}}`,
		`{"uuid":"x","type":"definitely-new-type-from-the-future"}`,
		`{"uuid":"a1","sessionId":"s","timestamp":"2026-06-12T10:00:01.000Z","type":"assistant","message":{"role":"model","parts":[{"text":"hello"}]}}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	src := New(dir)
	refs, _ := src.List(context.Background(), time.Time{})
	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 surviving messages, got %d", len(s.Messages))
	}
}

func TestRawPreserved(t *testing.T) {
	dir := t.TempDir()
	seedSession(t, dir)
	src := New(dir)
	refs, _ := src.List(context.Background(), time.Time{})
	s, _ := src.Load(context.Background(), refs[0])

	var got map[string]any
	if err := json.Unmarshal(s.Messages[0].Raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["uuid"] != "u1" || got["type"] != "user" {
		t.Errorf("raw fields lost: %v", got)
	}
}
