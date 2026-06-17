package kimicode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestWriteReadBack(t *testing.T) {
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	s := New("") // home arg unused when KIMI_CODE_HOME is set
	ctx := context.Background()
	in := &model.Session{
		ID: "kimi-code/xyz", Agent: "kimi-code", SourceID: "xyz",
		Title: "graphql migration", CWD: "/root/code/demo",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "把 rest 转 graphql", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "好，先生成 schema", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "kimi --session ") {
		t.Errorf("resume cmd lacks kimi --session: %q", res.ResumeCommand)
	}
	if res.TargetFile == "" {
		t.Error("WriteResult.TargetFile should be set")
	}
	refs, err := s.List(ctx, time.Time{})
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	out, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("session is nil")
	}
	// state.json carries title with [tape] prefix
	if !strings.Contains(out.Title, "[tape]") {
		t.Errorf("title should carry [tape]: %q", out.Title)
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd not round-tripped: %q", out.CWD)
	}
	// Two surviving messages: the (annotated) user input and the assistant reply
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != model.RoleUser || !strings.Contains(out.Messages[0].Text, "把 rest 转 graphql") {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("msg1 role = %v", out.Messages[1].Role)
	}
}
