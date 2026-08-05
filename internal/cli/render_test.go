package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/tape/internal/agentid"
	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/core/service"
)

// Regression: tape search showed mis-aligned rows when the result set
// mixed short agent names (cursor, codex) with long ones (claude-code,
// antigravity). shortIDColW must be wide enough for every registered
// agent's "name/XXXXXX…XXXX" abbreviated id, or padRightDisp returns
// the string unchanged and every column to its right slides over.
func TestShortIDColumnFitsEveryAgentName(t *testing.T) {
	// abbrev branch in shortID always emits 6+1+4 = 11 runes; +1 for
	// the '/'. The agent name itself contributes len(name) runes.
	const abbrevPlusSep = 12
	for _, name := range agentid.Canonicals() {
		id := name + "/abcdef0123456789abcdef"
		short := shortID(id)
		width := utf8.RuneCountInString(short)
		if width != utf8.RuneCountInString(name)+abbrevPlusSep {
			t.Fatalf("shortID changed shape for %q: %q (want %d runes)",
				name, short, utf8.RuneCountInString(name)+abbrevPlusSep)
		}
		if width > shortIDColW {
			t.Errorf("agent %q produces a %d-rune shortID %q "+
				"but shortIDColW is %d — every picker / table that uses "+
				"padRightDisp(shortID(...), shortIDColW) will mis-align "+
				"on rows from this agent. Bump shortIDColW.",
				name, width, short, shortIDColW)
		}
	}
}

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
	out := captureStdout(t, func() { renderHits(app, hits, "备份", 1, false, renderOpts{}) })

	// the two codex hits share a single header row. shortID renders
	// long sourceIDs as "head6 + … + tail4" so we look for the abbrev.
	if strings.Count(out, "codex/019e97…aaaa") != 1 {
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
	out := captureStdout(t, func() { renderHits(app, hits, "", 1, false, renderOpts{}) })
	if strings.Contains(out, "\n\n\t") || strings.Contains(out, "line2     ") {
		t.Errorf("whitespace not collapsed:\n%s", out)
	}
	if !strings.Contains(out, "line1 line2 line3") {
		t.Errorf("expected one-line snippet:\n%s", out)
	}
}

// TestRenderHitsExpandWrapsLongBodies pins the --expand contract: a
// long single-message body is split into multiple wrapped lines, each
// prefixed by the continuation indent (so visual alignment under the
// role column survives), and the query term still gets highlighted on
// the line it actually appears.
func TestRenderHitsExpandWrapsLongBodies(t *testing.T) {
	app := plainApp()
	long := strings.Repeat("alpha beta gamma delta ", 12) + "redis " + strings.Repeat("zeta eta theta ", 12)
	hits := []ports.Hit{{
		SessionID: "codex/abc", Agent: "codex",
		MessageID: "m1", Role: "user", Snippet: long,
	}}
	out := captureStdout(t, func() {
		renderHits(app, hits, "redis", 1, false, renderOpts{
			snippetWidth: 60, // small, force multiple wraps
			expand:       true,
		})
	})
	bodyLines := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "alpha") || strings.Contains(ln, "redis") || strings.Contains(ln, "zeta") {
			bodyLines++
		}
	}
	if bodyLines < 3 {
		t.Fatalf("expected >=3 wrapped body lines, got %d:\n%s", bodyLines, out)
	}
}

// TestRenderHitsContextShowsAdjacentTurns drives the -C N path with a
// hand-built session and verifies pre/post markers (↑1, ↓1) plus the
// dimmed role labels appear, while the hit message itself is NOT
// duplicated in the context window.
func TestRenderHitsContextShowsAdjacentTurns(t *testing.T) {
	app := plainApp()
	sess := &model.Session{
		ID: "codex/ctx", Agent: "codex",
		Messages: []model.Message{
			{ID: "m0", Role: "user", Text: "earlier user question"},
			{ID: "m1", Role: "assistant", Text: "matching redis answer"},
			{ID: "m2", Role: "user", Text: "follow-up question"},
		},
	}
	hits := []ports.Hit{{
		SessionID: "codex/ctx", Agent: "codex",
		MessageID: "m1", Role: "assistant",
		Snippet: "matching redis answer",
	}}
	out := captureStdout(t, func() {
		renderHits(app, hits, "redis", 1, false, renderOpts{
			context: 1,
			archive: stubGet(sess),
		})
	})
	if !strings.Contains(out, "↑1") || !strings.Contains(out, "↓1") {
		t.Errorf("missing context markers:\n%s", out)
	}
	if !strings.Contains(out, "earlier user question") {
		t.Errorf("pre-context missing:\n%s", out)
	}
	if !strings.Contains(out, "follow-up question") {
		t.Errorf("post-context missing:\n%s", out)
	}
	// hit line must appear exactly once; context window skips offset 0
	if strings.Count(out, "matching redis answer") != 1 {
		t.Errorf("hit line should appear once, got:\n%s", out)
	}
}

// stubGet is a tiny in-memory archive that returns a fixed session
// regardless of id. Lets renderHits exercise its "fetch full session
// for context/expand" branch without spinning up the real local
// archive on disk.
type stubArchive struct{ s *model.Session }

func stubGet(s *model.Session) stubArchive { return stubArchive{s: s} }
func (a stubArchive) Get(_ context.Context, _ string) (*model.Session, error) {
	return a.s, nil
}

// TestWrapDisplayWidthCJK ensures CJK runes (2 cells each) are counted
// correctly when wrapping; a naive byte-or-rune count would let a line
// overflow by a column.
func TestWrapDisplayWidthCJK(t *testing.T) {
	got := wrapDisplayWidth("你好世界你好世界你好世界", 8)
	if len(got) < 2 {
		t.Fatalf("expected multiple lines, got %v", got)
	}
	for _, line := range got {
		width := 0
		for _, r := range line {
			width += runeDisplayWidth(r)
		}
		if width > 8 {
			t.Errorf("line %q exceeds width 8 (got %d)", line, width)
		}
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
