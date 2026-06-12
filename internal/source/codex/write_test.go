package codex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestWriteReadBack(t *testing.T) {
	s := &Source{dir: t.TempDir()}
	ctx := context.Background()
	in := &model.Session{
		ID: "claude-code/abc", Agent: "claude-code", SourceID: "abc",
		CWD: "/root/code/demo",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "把认证改成 JWT"},
			{Role: model.RoleAssistant, Text: "已完成,见 auth/jwt.go"},
		},
	}
	resumeCmd, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumeCmd, "codex resume ") {
		t.Errorf("resume cmd = %q", resumeCmd)
	}
	refs, err := s.List(ctx, time.Time{})
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	out, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd = %q", out.CWD)
	}
	if len(out.Messages) != 2 || out.Messages[0].Text != "把认证改成 JWT" {
		t.Fatalf("messages = %+v", out.Messages)
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("role = %s", out.Messages[1].Role)
	}
}
