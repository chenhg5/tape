package codex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestLoadFixture(t *testing.T) {
	s := &Source{dir: "testdata/sessions"}
	ctx := context.Background()

	refs, err := s.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 ref, got %d", len(refs))
	}
	if refs[0].SourceID != "019ea0af-3d6a-7393-8ccf-a4ae49f116c3" {
		t.Errorf("sourceID from filename = %q", refs[0].SourceID)
	}
	sess, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sess.CWD != "/root/code/demo" || sess.GitBranch != "main" || sess.Model != "gpt-5.3-codex" {
		t.Errorf("meta: cwd=%q branch=%q model=%q", sess.CWD, sess.GitBranch, sess.Model)
	}
	// user message + function_call message + assistant message
	if len(sess.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(sess.Messages))
	}
	if sess.Messages[0].Role != model.RoleUser || sess.Messages[0].Text != "帮我提交代码" {
		t.Errorf("environment_context should be stripped, got %q", sess.Messages[0].Text)
	}
	fc := sess.Messages[1]
	if len(fc.ToolCalls) != 1 || fc.ToolCalls[0].Name != "shell" {
		t.Fatalf("function_call: %+v", fc)
	}
	if !strings.Contains(fc.ToolCalls[0].Output, "On branch main") {
		t.Errorf("output not attached by call_id: %q", fc.ToolCalls[0].Output)
	}
	if sess.Title != "帮我提交代码" {
		t.Errorf("title = %q", sess.Title)
	}
}
