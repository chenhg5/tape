package sqlitefts

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

func TestIndexSearchCJKAndLatin(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	sess := &model.Session{
		ID: "claude-code/abc", Agent: "claude-code", SourceID: "abc",
		Title: "auth design", CWD: "/root/code/demo",
		StartedAt: now, UpdatedAt: now,
		Messages: []model.Message{
			{ID: "m1", Role: model.RoleUser, Text: "为什么我们的会话备份方案不用 OAuth2?", Timestamp: now},
			{ID: "m2", Role: model.RoleAssistant, Text: "Because JWT is simpler for this case.", Timestamp: now},
		},
	}
	if err := ix.Upsert(ctx, sess); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"会话备份", "OAuth2", "备份方案", "jwt"} {
		hits, err := ix.Search(ctx, ports.Query{Text: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) == 0 {
			t.Errorf("search %q: want hits, got none", q)
		}
	}

	// non-existent term
	hits, err := ix.Search(ctx, ports.Query{Text: "kubernetes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("want 0 hits, got %d", len(hits))
	}

	// agent filter excludes
	hits, err = ix.Search(ctx, ports.Query{Text: "jwt", Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("agent filter: want 0 hits, got %d", len(hits))
	}

	// upsert is idempotent (no duplicate rows)
	if err := ix.Upsert(ctx, sess); err != nil {
		t.Fatal(err)
	}
	hits, _ = ix.Search(ctx, ports.Query{Text: "jwt"})
	if len(hits) != 1 {
		t.Errorf("after re-upsert: want 1 hit, got %d", len(hits))
	}
}

func TestSearchFilters(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	put := func(id, agent, cwd string, ts time.Time) {
		s := &model.Session{
			ID: agent + "/" + id, Agent: agent, SourceID: id, CWD: cwd,
			StartedAt: ts, UpdatedAt: ts,
			Messages: []model.Message{{ID: "m", Role: model.RoleUser, Text: "shared keyword payload", Timestamp: ts}},
		}
		if err := ix.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	put("s1", "codex", "/root/code/alpha", old)
	put("s2", "claude-code", "/root/code/beta", recent)

	count := func(q ports.Query) int {
		q.Text = "payload"
		hits, err := ix.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if n := count(ports.Query{}); n != 2 {
		t.Errorf("unfiltered: %d", n)
	}
	if n := count(ports.Query{Agent: "codex"}); n != 1 {
		t.Errorf("agent: %d", n)
	}
	if n := count(ports.Query{Project: "/root/code/beta"}); n != 1 {
		t.Errorf("project: %d", n)
	}
	if n := count(ports.Query{Since: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}); n != 1 {
		t.Errorf("since: %d", n)
	}
	if n := count(ports.Query{Limit: 1}); n != 1 {
		t.Errorf("limit: %d", n)
	}
}

func TestSearchToolCallNamesAreIndexed(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()
	s := &model.Session{
		ID: "codex/t1", Agent: "codex", SourceID: "t1",
		Messages: []model.Message{{
			ID: "m1", Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{Name: "WebSearch", Input: `{"q":"kubernetes operator"}`}},
		}},
	}
	if err := ix.Upsert(ctx, s); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(ctx, ports.Query{Text: "kubernetes"})
	if err != nil || len(hits) != 1 {
		t.Errorf("tool input not searchable: %v %v", hits, err)
	}
}

func TestEmptyQueryRejected(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Search(context.Background(), ports.Query{Text: "!!!"}); err == nil {
		t.Error("punctuation-only query must be rejected")
	}
}

func TestMakeSnippet(t *testing.T) {
	long := strings.Repeat("填充内容 ", 200) + "目标词组出现在这里" + strings.Repeat(" 尾部内容", 200)
	snip := makeSnippet(long, "目标词组")
	if !strings.Contains(snip, "目标词组") {
		t.Errorf("snippet lost the match: %q", snip)
	}
	if len(snip) > 300 {
		t.Errorf("snippet too long: %d bytes", len(snip))
	}
	if !utf8.ValidString(snip) {
		t.Errorf("snippet split runes: %q", snip)
	}
	if !strings.HasPrefix(snip, "…") || !strings.HasSuffix(snip, "…") {
		t.Errorf("expected ellipses on both ends: %q", snip)
	}
	// match at position 0 gets no leading ellipsis
	if s := makeSnippet("short text", "short"); s != "short text" {
		t.Errorf("got %q", s)
	}
}
