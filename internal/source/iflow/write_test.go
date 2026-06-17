package iflow

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
		ID: "iflow/xyz", Agent: "iflow", SourceID: "xyz",
		Title: "rate limit bug", CWD: "/root/code/demo",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "限流为什么不生效", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "需要在 nginx 层加 limit_req", Timestamp: time.Now()},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "iflow") {
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
	if out.Agent != "iflow" {
		t.Errorf("agent: %q", out.Agent)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(out.Messages))
	}
	if out.Messages[0].Text != "限流为什么不生效" {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
}
