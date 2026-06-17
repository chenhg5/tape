package claudecode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// Round-trip: a session written natively must parse back with our own reader.
func TestWriteReadBack(t *testing.T) {
	s := &Source{dir: t.TempDir()}
	ctx := context.Background()
	in := &model.Session{
		ID: "codex/xyz", Agent: "codex", SourceID: "xyz",
		Title: "JWT migration", CWD: "/root/code/demo", GitBranch: "main",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "把认证改成 JWT", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "已完成,见 auth/jwt.go", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "claude --resume ") {
		t.Errorf("resume cmd = %q", res.ResumeCommand)
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
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 messages back, got %d", len(out.Messages))
	}
	if out.Messages[0].Text != "把认证改成 JWT" || out.Messages[0].Role != model.RoleUser {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.CWD != "/root/code/demo" || !strings.HasPrefix(out.Title, "[tape]") {
		t.Errorf("cwd=%q title=%q", out.CWD, out.Title)
	}
	// messages must be parent-chained for claude's tree model
	if out.Messages[1].ParentID != out.Messages[0].ID {
		t.Errorf("parent chain broken: %q != %q", out.Messages[1].ParentID, out.Messages[0].ID)
	}
}
