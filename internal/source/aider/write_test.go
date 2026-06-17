package aider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestWriteReadBack(t *testing.T) {
	cwd := t.TempDir()
	s := New(t.TempDir()) // home (for global file) unused — we write to project cwd
	ctx := context.Background()
	in := &model.Session{
		ID: "aider/xyz", Agent: "aider", SourceID: "xyz",
		Title: "fix flaky test", CWD: cwd,
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "fix the flaky retry test"},
			{Role: model.RoleAssistant, Text: "Looking at the test now"},
			{Role: model.RoleTool, Text: "running pytest..."},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "aider") {
		t.Errorf("resume cmd: %q", res.ResumeCommand)
	}
	if res.TargetFile == "" {
		t.Error("WriteResult.TargetFile should be set")
	}

	// Read back via aider's own parser. We must seed the working dir
	// for List() — aider's candidates() includes os.Getwd().
	t.Chdir(cwd)

	refs, err := s.List(ctx, time.Time{})
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	out, err := s.Load(ctx, refs[0])
	if err != nil || out == nil {
		t.Fatalf("load: out=%v err=%v", out, err)
	}
	// We wrote: [tape] marker (tool), user, assistant, tool
	if len(out.Messages) < 3 {
		t.Fatalf("want >=3 msgs, got %d: %+v", len(out.Messages), out.Messages)
	}
	// First should be the [tape] marker as a tool message.
	if !strings.Contains(out.Messages[0].Text, "[tape]") {
		t.Errorf("first message should carry [tape] marker: %q", out.Messages[0].Text)
	}
	// User and assistant survived the round-trip.
	var sawUser, sawAssistant bool
	for _, m := range out.Messages {
		if m.Role == model.RoleUser && strings.Contains(m.Text, "fix the flaky retry test") {
			sawUser = true
		}
		if m.Role == model.RoleAssistant && strings.Contains(m.Text, "Looking at the test now") {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Errorf("user/assistant not round-tripped: user=%v assistant=%v", sawUser, sawAssistant)
	}
}

func TestWriteAppendsToExistingFile(t *testing.T) {
	cwd := t.TempDir()
	s := New(t.TempDir())
	ctx := context.Background()

	first := &model.Session{
		ID: "aider/a", Agent: "aider", SourceID: "a", CWD: cwd, Title: "session A",
		Messages: []model.Message{{Role: model.RoleUser, Text: "first"}},
	}
	if _, err := s.Write(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &model.Session{
		ID: "aider/b", Agent: "aider", SourceID: "b", CWD: cwd, Title: "session B",
		Messages: []model.Message{{Role: model.RoleUser, Text: "second"}},
	}
	if _, err := s.Write(ctx, second); err != nil {
		t.Fatal(err)
	}

	t.Chdir(cwd)
	refs, _ := s.List(ctx, time.Time{})
	if len(refs) != 2 {
		t.Fatalf("appending two sessions should yield 2 refs, got %d", len(refs))
	}
}
