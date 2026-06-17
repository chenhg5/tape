package qoder

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
		ID: "qoder/xyz", Agent: "qoder", SourceID: "xyz",
		Title: "rewrite auth", CWD: "/root/code/demo", Model: "qwen-plus",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "把 session 换成 jwt", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "好，先看现有 handler", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "qodercli -r ") {
		t.Errorf("resume cmd: %q", res.ResumeCommand)
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
	if out.Agent != agentName {
		t.Errorf("agent slug: %q", out.Agent)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(out.Messages))
	}
	if !strings.Contains(out.Messages[0].Text, "把 session 换成 jwt") {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd not round-tripped: %q", out.CWD)
	}
}
