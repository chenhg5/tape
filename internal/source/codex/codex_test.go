package codex

import (
	"context"
	"os"
	"path/filepath"
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

func TestSourceIDFromFilename(t *testing.T) {
	cases := map[string]string{
		"/x/rollout-2026-06-07T14-04-59-019ea0af-3d6a-7393-8ccf-a4ae49f116c3.jsonl": "019ea0af-3d6a-7393-8ccf-a4ae49f116c3",
		"/x/rollout-short.jsonl": "short",
	}
	for in, want := range cases {
		if got := sourceID(in); got != want {
			t.Errorf("sourceID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContentTextVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[{"type":"input_text","text":"hello"}]`, "hello"},
		{`[{"type":"input_text","text":"<environment_context>x</environment_context>"},{"type":"input_text","text":"real"}]`, "real"},
		{`"plain string"`, "plain string"},
		{`12345`, ""},
		{`[]`, ""},
	}
	for _, c := range cases {
		if got := contentText([]byte(c.in)); got != c.want {
			t.Errorf("contentText(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutputTextVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"raw output"`, "raw output"},
		{`{"output":"from output field"}`, "from output field"},
		{`{"content":"from content field"}`, "from content field"},
		{`[1,2]`, ""},
	}
	for _, c := range cases {
		if got := outputText([]byte(c.in)); got != c.want {
			t.Errorf("outputText(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRole(t *testing.T) {
	if normalizeRole("developer") != "system" || normalizeRole("user") != "user" {
		t.Error("role normalization broken")
	}
}

func TestDetectMissingDir(t *testing.T) {
	s := New(t.TempDir()) // no .codex inside
	found, _, err := s.Detect(context.Background())
	if found || err != nil {
		t.Errorf("found=%v err=%v", found, err)
	}
}

// Write must template session_meta from the newest real session so that
// cli_version and enum fields match the installed codex version.
func TestWriteUsesRealSessionMetaTemplate(t *testing.T) {
	dir := t.TempDir()
	s := &Source{dir: dir}
	real := filepath.Join(dir, "2026", "06", "01")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"timestamp":"2026-06-01T00:00:00.000Z","type":"session_meta","payload":{"id":"old","timestamp":"old-ts","cwd":"/old","originator":"codex_exec","cli_version":"9.9.9","source":"exec","thread_source":"user","model_provider":"openai","git":{"branch":"stale"}}}`
	if err := os.WriteFile(filepath.Join(real, "rollout-2026-06-01T00-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}

	payload := s.sessionMetaPayload("new-id", "new-ts", "/new/cwd")
	if payload["cli_version"] != "9.9.9" {
		t.Errorf("template cli_version not inherited: %v", payload["cli_version"])
	}
	if payload["id"] != "new-id" || payload["cwd"] != "/new/cwd" || payload["timestamp"] != "new-ts" {
		t.Errorf("overrides not applied: %v", payload)
	}
	if _, hasGit := payload["git"]; hasGit {
		t.Error("stale git info must be dropped")
	}
}

func TestSessionMetaFallbackWithoutTemplate(t *testing.T) {
	s := &Source{dir: t.TempDir()}
	payload := s.sessionMetaPayload("id1", "ts1", "/cwd1")
	for _, key := range []string{"id", "timestamp", "cwd", "originator", "cli_version", "source", "thread_source", "model_provider"} {
		if payload[key] == "" || payload[key] == nil {
			t.Errorf("fallback payload missing %s", key)
		}
	}
}
