package restore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func demoSession() *model.Session {
	now := time.Now()
	return &model.Session{
		ID: "claude-code/abc", Agent: "claude-code", CWD: "/root/code/demo", GitBranch: "main",
		StartedAt: now.Add(-time.Hour), UpdatedAt: now,
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "帮我把认证从 OAuth2 改成 JWT"},
			{Role: model.RoleAssistant, Text: "好的,先看一下现有实现。",
				ToolCalls: []model.ToolCall{{Name: "Read", Input: `{"path":"/root/code/demo/auth/oauth.go"}`}}},
			{Role: model.RoleTool, Text: "package auth ..."},
			{Role: model.RoleAssistant, Text: "已完成改造,JWT 中间件在 auth/jwt.go,下一步需要补测试。"},
		},
	}
}

func TestTemplateBrief(t *testing.T) {
	doc, method, err := Brief(context.Background(), demoSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if method != "template" {
		t.Errorf("method = %q", method)
	}
	for _, want := range []string{
		"帮我把认证从 OAuth2 改成 JWT",       // goal
		"/root/code/demo/auth/oauth.go", // touched file
		"下一步需要补测试",                      // last assistant message
		"`/root/code/demo`",             // cwd
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("brief missing %q:\n%s", want, doc)
		}
	}
}

func TestTranscriptBudget(t *testing.T) {
	s := demoSession()
	for i := 0; i < 200; i++ {
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Text: strings.Repeat("长内容x", 100)})
	}
	tr := transcript(s, 5000)
	if len(tr) > 8000 {
		t.Errorf("transcript over budget: %d bytes", len(tr))
	}
	if !strings.Contains(tr, "earlier messages omitted") {
		t.Error("expected omission marker")
	}
}
