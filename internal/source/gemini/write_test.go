package gemini

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
		ID: "gemini/xyz", Agent: "gemini", SourceID: "xyz",
		Title: "build is slow", CWD: "/root/code/demo", Model: "gemini-2.5-pro",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "为什么 build 越来越慢", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "先量测下 webpack profile", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "gemini") || !strings.Contains(res.ResumeCommand, "/root/code/demo") {
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
		t.Fatalf("want 2 messages back, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != model.RoleUser || out.Messages[0].Text != "为什么 build 越来越慢" {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("msg1 role = %v", out.Messages[1].Role)
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd not round-tripped: %q", out.CWD)
	}
	if !strings.Contains(out.Title, "[tape]") {
		t.Errorf("title should carry [tape] marker, got %q", out.Title)
	}
}

func TestWriteUsesProjectHashFromCWD(t *testing.T) {
	if h := projectHash("/root/code/demo"); len(h) != 12 {
		t.Errorf("projectHash length = %d, want 12", len(h))
	}
	if projectHash("/a") == projectHash("/b") {
		t.Error("projectHash should differ across paths")
	}
	if projectHash("/same") != projectHash("/same") {
		t.Error("projectHash should be deterministic")
	}
}
