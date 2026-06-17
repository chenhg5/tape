package antigravity

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
		ID: "antigravity/xyz", Agent: "antigravity", SourceID: "xyz",
		Title: "cloud run quickstart", CWD: "/root/code/demo",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "怎么部署到 cloud run", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "先 gcloud run deploy --source .", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "agy --conversation ") {
		t.Errorf("resume cmd lacks agy --conversation: %q", res.ResumeCommand)
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
	// We wrote: marker USER_INPUT + user msg + assistant msg = 3 surviving lines
	// (tool RoleTool is filtered before write).
	if len(out.Messages) != 3 {
		t.Fatalf("want 3 msgs (marker + user + assistant), got %d: %+v", len(out.Messages), out.Messages)
	}
	if !strings.HasPrefix(out.Messages[0].Text, "[tape]") {
		t.Errorf("first message should carry [tape] marker: %q", out.Messages[0].Text)
	}
	if out.Messages[1].Role != model.RoleUser || out.Messages[1].Text != "怎么部署到 cloud run" {
		t.Errorf("user msg: %+v", out.Messages[1])
	}
	if out.Messages[2].Role != model.RoleAssistant {
		t.Errorf("assistant role: %v", out.Messages[2].Role)
	}
}
