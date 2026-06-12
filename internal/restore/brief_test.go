package restore

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		"帮我把认证从 OAuth2 改成 JWT",          // goal
		"/root/code/demo/auth/oauth.go", // touched file
		"下一步需要补测试",                      // last assistant message
		"`/root/code/demo`",             // cwd
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("brief missing %q:\n%s", want, doc)
		}
	}
}

type fakeRunner struct {
	out string
	err error
	got string
}

func (f *fakeRunner) Name() string    { return "fake" }
func (f *fakeRunner) Available() bool { return true }
func (f *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	f.got = prompt
	return f.out, f.err
}

func TestBriefUsesRunner(t *testing.T) {
	r := &fakeRunner{out: "# Handoff\nLLM 写的交接文档"}
	doc, method, err := Brief(context.Background(), demoSession(), r)
	if err != nil {
		t.Fatal(err)
	}
	if method != "llm:fake" || doc != r.out {
		t.Errorf("method=%q doc=%q", method, doc)
	}
	// the prompt must carry metadata and the dialogue
	for _, want := range []string{"cwd=/root/code/demo", "OAuth2 改成 JWT", "## Next"} {
		if !strings.Contains(r.got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBriefFallsBackOnRunnerError(t *testing.T) {
	r := &fakeRunner{err: context.DeadlineExceeded}
	doc, method, err := Brief(context.Background(), demoSession(), r)
	if err != nil {
		t.Fatal(err)
	}
	if method != "template" || !strings.Contains(doc, "# Handoff") {
		t.Errorf("expected template fallback, method=%q", method)
	}
}

func TestBriefFallsBackOnEmptyOutput(t *testing.T) {
	r := &fakeRunner{out: "   \n"}
	_, method, _ := Brief(context.Background(), demoSession(), r)
	if method != "template" {
		t.Errorf("empty LLM output must fall back, method=%q", method)
	}
}

func TestTouchedFilesDedupeAndCap(t *testing.T) {
	s := demoSession()
	for i := 0; i < 50; i++ {
		s.Messages = append(s.Messages, model.Message{
			Role:      model.RoleAssistant,
			ToolCalls: []model.ToolCall{{Name: "Read", Input: `{"path":"/repo/pkg/file` + string(rune('a'+i%26)) + `.go"}`}},
		})
	}
	files := touchedFiles(s)
	if len(files) > 30 {
		t.Errorf("cap exceeded: %d", len(files))
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			t.Errorf("duplicate %s", f)
		}
		seen[f] = true
	}
}

func TestClipUTF8Safety(t *testing.T) {
	s := strings.Repeat("中", 100)
	for max := 1; max < 12; max++ {
		if out := clip(s, max); !utf8.ValidString(out) {
			t.Errorf("clip(%d) split a rune: %q", max, out)
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
