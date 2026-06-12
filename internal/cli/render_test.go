package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/core/service"
)

// captureStdout swaps os.Stdout while fn runs and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// plainApp produces a never-color, never-JSON renderer that we can probe.
func plainApp() *App { return &App{} }

func TestRenderHitsBasic(t *testing.T) {
	app := plainApp()
	hits := []ports.Hit{
		{SessionID: "codex/019e970a-aaaa", Agent: "codex", Project: "p",
			Title: "Networking failure", Role: "user", Snippet: "我已 备份 hosts file",
			Timestamp: time.Now().Add(-30 * time.Minute)},
		{SessionID: "codex/019e970a-aaaa", Agent: "codex", Project: "p",
			Title: "Networking failure", Role: "assistant", Snippet: "备份 完成",
			Timestamp: time.Now().Add(-20 * time.Minute)},
		{SessionID: "cursor/abcd1234-bbbb", Agent: "cursor", Project: "p",
			Title: "Doc advisor", Role: "tool", Snippet: "备份 方案"},
	}
	out := captureStdout(t, func() { renderHits(app, hits, "备份", 1, false) })

	// the two codex hits share a single header row
	if strings.Count(out, "codex/019e970a") != 1 {
		t.Errorf("codex header not deduped:\n%s", out)
	}
	// both hits show up with their roles
	for _, want := range []string{"user", "assistant", "tool", "Networking failure", "Doc advisor"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// summary line distinguishes hits and sessions
	if !strings.Contains(out, "3 hit(s) in 2 sessions") {
		t.Errorf("summary missing:\n%s", out)
	}
}

func TestRenderHitsCollapsesWhitespace(t *testing.T) {
	app := plainApp()
	hits := []ports.Hit{
		{SessionID: "codex/x", Agent: "codex", Role: "user",
			Snippet: "line1\n\n\tline2     line3"},
	}
	out := captureStdout(t, func() { renderHits(app, hits, "", 1, false) })
	if strings.Contains(out, "\n\n\t") || strings.Contains(out, "line2     ") {
		t.Errorf("whitespace not collapsed:\n%s", out)
	}
	if !strings.Contains(out, "line1 line2 line3") {
		t.Errorf("expected one-line snippet:\n%s", out)
	}
}

func TestQueryTermsStripsOperators(t *testing.T) {
	got := queryTerms(`"foo bar" AND baz NOT qux*`)
	want := []string{"foo", "bar", "baz", "qux"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("queryTerms = %v, want %v", got, want)
	}
	if got := queryTerms(""); len(got) != 0 {
		t.Errorf("empty query: %v", got)
	}
}

func TestCaseInsensitiveReplace(t *testing.T) {
	got := caseInsensitiveReplace("Foo FOO foo", "foo", "[X]")
	if got != "[X] [X] [X]" {
		t.Errorf("got %q", got)
	}
	if got := caseInsensitiveReplace("no match", "xyz", "Y"); got != "no match" {
		t.Errorf("no-match: %q", got)
	}
	if got := caseInsensitiveReplace("anything", "", "Y"); got != "anything" {
		t.Errorf("empty needle must no-op: %q", got)
	}
}

func TestRenderSessionList(t *testing.T) {
	app := plainApp()
	now := time.Now()
	sums := []model.Summary{
		{ID: "codex/019ea0af-x", Agent: "codex", Project: "p1",
			Title: "T1", MsgCount: 12, UpdatedAt: now.Add(-time.Hour)},
		{ID: "claude-code/abc-y", Agent: "claude-code", Project: "p2",
			Title: "T2", MsgCount: 5, UpdatedAt: now.Add(-48 * time.Hour)},
	}
	out := captureStdout(t, func() { renderSessionList(app, sums, 1, 20, len(sums)) })
	for _, want := range []string{"ID", "AGENT", "UPDATED", "MSGS", "TITLE", "codex/019ea0af", "T1", "T2", "2 session(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSyncReport(t *testing.T) {
	app := plainApp()
	rep := service.SyncReport{Sources: []service.SourceReport{
		{Agent: "claude-code", Found: true, Scanned: 10, Archived: 2, Skipped: 8},
		{Agent: "codex", Found: true, Host: "dev@build", Scanned: 5, Skipped: 5},
		{Agent: "cursor", Found: false},
		{Agent: "broken", Found: true, Errors: []string{"boom"}},
	}}
	out := captureStdout(t, func() { renderSyncReport(app, rep) })
	for _, want := range []string{
		"claude-code", "scanned", "archived", "skipped",
		"codex@dev@build", // host in label
		"not installed",   // missing agent
		"2 new session(s) archived",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// nothing archived: prints "nothing new"
	out = captureStdout(t, func() {
		renderSyncReport(app, service.SyncReport{Sources: []service.SourceReport{
			{Agent: "codex", Found: true, Skipped: 3},
		}})
	})
	if !strings.Contains(out, "nothing new") {
		t.Errorf("expected 'nothing new', got:\n%s", out)
	}

	// no agents found at all
	out = captureStdout(t, func() {
		renderSyncReport(app, service.SyncReport{Sources: []service.SourceReport{
			{Agent: "codex", Found: false},
		}})
	})
	if !strings.Contains(out, "no agent data found") {
		t.Errorf("expected empty-machine summary, got:\n%s", out)
	}
}

func TestRenderSession(t *testing.T) {
	app := plainApp()
	ts := time.Now().Add(-10 * time.Minute)
	s := &model.Session{
		ID: "codex/x", Agent: "codex", Title: "Auth migration",
		CWD: "/r/p", GitBranch: "main", Model: "gpt-5",
		StartedAt: ts, UpdatedAt: ts.Add(5 * time.Minute),
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "why jwt?", Timestamp: ts},
			{Role: model.RoleAssistant, Text: "stateless", Timestamp: ts.Add(time.Minute),
				ToolCalls: []model.ToolCall{{Name: "Read", Input: `{"p":"a.go"}`}}},
			{Role: model.RoleTool, Text: "tool output, full mode only"},
		},
		Meta: map[string]string{"host": "dev@build"},
	}
	out := captureStdout(t, func() { renderSession(app, s, false) })
	for _, want := range []string{"Auth migration", "id", "codex/x", "/r/p", "user", "why jwt?", "assistant", "stateless", "Read", "host", "dev@build"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tool output") {
		t.Error("tool message must be hidden without --full")
	}

	// --full surfaces tool output
	out = captureStdout(t, func() { renderSession(app, s, true) })
	if !strings.Contains(out, "tool output, full mode only") {
		t.Error("--full must show tool output")
	}
}

func TestHighlightSnippetTermFound(t *testing.T) {
	app := plainApp() // no color: highlight is plain substitution
	// without color, casing of the matched span follows the query term.
	// With color (in real terminals) the highlighted span uses the
	// original casing from the snippet via ANSI wrapping. The unit test
	// just ensures the term survives and the snippet is unchanged length.
	got := highlightSnippet(app, "the quick BROWN fox", "brown", 100)
	if !strings.Contains(strings.ToLower(got), "brown") {
		t.Errorf("highlight removed the term: %q", got)
	}
	if !strings.Contains(highlightSnippet(app, "abcdef", "", 10), "abcdef") {
		t.Error("empty query must not error")
	}
}
