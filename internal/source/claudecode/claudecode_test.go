package claudecode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestLoadFixture(t *testing.T) {
	s := &Source{dir: "testdata/projects"}
	ctx := context.Background()

	refs, err := s.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 ref, got %d", len(refs))
	}
	sess, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "Demo auth discussion" {
		t.Errorf("title = %q", sess.Title)
	}
	if sess.CWD != "/root/code/demo" || sess.GitBranch != "main" {
		t.Errorf("cwd/branch = %q/%q", sess.CWD, sess.GitBranch)
	}
	// sidechain excluded: user + assistant + tool_result
	if len(sess.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(sess.Messages), sess.Messages)
	}
	if sess.Messages[0].Role != model.RoleUser || sess.Messages[0].Text != "为什么不用 OAuth2?" {
		t.Errorf("msg0 = %+v", sess.Messages[0])
	}
	a := sess.Messages[1]
	if a.Role != model.RoleAssistant || len(a.ToolCalls) != 1 || a.ToolCalls[0].Name != "Read" {
		t.Errorf("msg1 = %+v", a)
	}
	if sess.Messages[2].Role != model.RoleTool {
		t.Errorf("tool_result-only user line should normalize to role tool, got %s", sess.Messages[2].Role)
	}
	if sess.StartedAt.IsZero() || !sess.UpdatedAt.After(sess.StartedAt) {
		t.Errorf("times: %v..%v", sess.StartedAt, sess.UpdatedAt)
	}
}
