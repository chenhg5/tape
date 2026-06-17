package qwen

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestWriteReadBack(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	ctx := context.Background()
	in := &model.Session{
		ID: "qwen/xyz", Agent: "qwen", SourceID: "xyz",
		Title: "jwt migration", CWD: "/root/code/demo", GitBranch: "main",
		Model: "qwen2.5-coder-30b",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "实现 jwt 鉴权", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "先看现有 auth 中间件", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "qwen") || !strings.Contains(res.ResumeCommand, "/root/code/demo") {
		t.Errorf("resume cmd missing bin or cwd: %q", res.ResumeCommand)
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
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != model.RoleUser || !strings.Contains(out.Messages[0].Text, "实现 jwt 鉴权") {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("msg1 role = %v", out.Messages[1].Role)
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd not round-tripped: %q", out.CWD)
	}
	// parent chain should link assistant -> user via parentUuid
	if out.Messages[1].ParentID != out.Messages[0].ID {
		t.Errorf("parent chain broken: %q != %q", out.Messages[1].ParentID, out.Messages[0].ID)
	}
}
