package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func writeSession(t *testing.T, dir, project, id string, lines []string) {
	t.Helper()
	p := filepath.Join(dir, project)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(p, id+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStringContentAndSummaryTitle(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "-root-p", "11111111-aaaa-bbbb-cccc-dddddddddddd", []string{
		`{"type":"summary","summary":"来自 summary 行的标题"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-06-01T01:00:00Z","cwd":"/root/p","message":{"role":"user","content":"纯字符串内容"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-06-01T01:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"答复"}]}}`,
		`this line is not json and must be skipped`,
		`{"type":"file-history-snapshot","irrelevant":true}`,
	})
	s := &Source{dir: dir}
	ctx := context.Background()
	refs, _ := s.List(ctx, time.Time{})
	sess, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "来自 summary 行的标题" {
		t.Errorf("summary title not used: %q", sess.Title)
	}
	if len(sess.Messages) != 2 || sess.Messages[0].Text != "纯字符串内容" {
		t.Errorf("messages: %+v", sess.Messages)
	}
	if sess.Messages[1].ParentID != "u1" {
		t.Errorf("parent chain lost: %+v", sess.Messages[1])
	}
}

func TestEmptySessionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "-root-p", "22222222-aaaa-bbbb-cccc-dddddddddddd", []string{
		`{"type":"file-history-snapshot"}`,
	})
	s := &Source{dir: dir}
	refs, _ := s.List(context.Background(), time.Time{})
	sess, err := s.Load(context.Background(), refs[0])
	if err != nil || sess != nil {
		t.Errorf("metadata-only session: sess=%v err=%v", sess, err)
	}
}

func TestListSinceFilter(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "-root-p", "33333333-aaaa-bbbb-cccc-dddddddddddd", []string{`{}`})
	s := &Source{dir: dir}
	refs, _ := s.List(context.Background(), time.Time{})
	if len(refs) != 1 {
		t.Fatalf("baseline: %d", len(refs))
	}
	refs, _ = s.List(context.Background(), time.Now().Add(time.Hour))
	if len(refs) != 0 {
		t.Errorf("future since must filter out everything, got %d", len(refs))
	}
}

func TestBlockText(t *testing.T) {
	if got := blockText(json.RawMessage(`"plain"`)); got != "plain" {
		t.Errorf("got %q", got)
	}
	if got := blockText(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "a\nb" {
		t.Errorf("got %q", got)
	}
	if got := blockText(json.RawMessage(`42`)); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestProjectDirName(t *testing.T) {
	cases := map[string]string{
		"/root/code/tape": "-root-code-tape",
		"/a/b.c":          "-a-b-c",
		"C:\\Users\\x y":  "C--Users-x-y",
	}
	for in, want := range cases {
		if got := projectDirName(in); got != want {
			t.Errorf("projectDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
