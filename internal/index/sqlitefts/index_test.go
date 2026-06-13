package sqlitefts

import (
	"context"
	"fmt"
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

	// The synthetic meta row puts agent/title/project into the index, so
	// `search codex` or `search "auth design"` find the session even when
	// the body of the messages never spells them out.
	for _, q := range []string{"claude-code", "auth design", "demo"} {
		hits, err := ix.Search(ctx, ports.Query{Text: q})
		if err != nil {
			t.Fatalf("meta search %q: %v", q, err)
		}
		if len(hits) == 0 {
			t.Errorf("meta search %q: want at least 1 hit (synthetic @meta row)", q)
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

	// Exclude axes: drop one agent / project / host. The exclude
	// predicates layer in as NOT clauses, so single-axis exclusion
	// is the inverse of single-axis positive selection.
	if n := count(ports.Query{ExcludeAgents: []string{"codex"}}); n != 1 {
		t.Errorf("exclude codex: %d", n)
	}
	if n := count(ports.Query{ExcludeProjects: []string{"/root/code/alpha"}}); n != 1 {
		t.Errorf("exclude alpha: %d", n)
	}
	// Multi-value exclude: drop both → zero hits.
	if n := count(ports.Query{ExcludeAgents: []string{"codex", "claude-code"}}); n != 0 {
		t.Errorf("exclude both: %d", n)
	}
}

// TestSearchExcludeHost: hosts use the same "local" sentinel as the
// positive filter, so ExcludeHosts=["local"] returns only sessions
// with a non-empty host. Needs its own setup because the shared
// TestSearchFilters fixture has no host stamps.
func TestSearchExcludeHost(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()
	put := func(id, agent, host string) {
		s := &model.Session{
			ID: agent + "/" + id, Agent: agent, SourceID: id,
			StartedAt: time.Unix(1700000000, 0),
			UpdatedAt: time.Unix(1700000000, 0),
			Messages:  []model.Message{{ID: "m", Role: model.RoleUser, Text: "shared keyword payload", Timestamp: time.Unix(1700000000, 0)}},
		}
		if host != "" {
			s.Meta = map[string]string{"host": host}
		}
		if err := ix.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	put("loc", "codex", "")
	put("a", "codex", "dev@host-a")
	put("b", "codex", "ci@host-b")

	count := func(q ports.Query) int {
		q.Text = "payload"
		hits, err := ix.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}

	if n := count(ports.Query{ExcludeHosts: []string{"local"}}); n != 2 {
		t.Errorf(`exclude "local": %d, want 2 (remote-only)`, n)
	}
	if n := count(ports.Query{ExcludeHosts: []string{"dev@host-a"}}); n != 2 {
		t.Errorf(`exclude host-a: %d, want 2`, n)
	}
	if n := count(ports.Query{ExcludeHosts: []string{"dev@host-a", "ci@host-b"}}); n != 1 {
		t.Errorf(`exclude both remotes: %d, want 1 (just local)`, n)
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

// Tool outputs are where most of the "evidence" lives in agent
// conversations (file reads, shell results). Without them in the index,
// search misses anything the assistant only saw via tool feedback —
// which is what was wrong with the original 'tape search codex' bug.
func TestSearchToolCallOutputsAreIndexed(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()
	s := &model.Session{
		ID: "codex/t2", Agent: "codex", SourceID: "t2",
		Messages: []model.Message{{
			ID: "m1", Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				Name:   "Read",
				Input:  `{"path":"main.go"}`,
				Output: "package main\n\n// orchestrates kubernetes operators",
			}},
		}},
	}
	if err := ix.Upsert(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"orchestrates", "package", "operators"} {
		hits, err := ix.Search(ctx, ports.Query{Text: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) == 0 {
			t.Errorf("tool output not searchable for %q", q)
		}
	}
}

// A fresh index never needs a rebuild; an index populated by an older
// version does; after MarkBuilt it doesn't again. This is what lets
// `tape sync` self-heal without anyone needing to know the index exists.
func TestWantsRebuildLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tape.db")
	ix, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()

	// brand-new install, no sessions yet → no rebuild needed
	if need, err := ix.WantsRebuild(ctx); err != nil || need {
		t.Fatalf("empty index wantsRebuild=%v err=%v", need, err)
	}

	// simulate "indexed by an older version": data present, no marker
	now := time.Now().UTC()
	sess := &model.Session{
		ID: "codex/x1", Agent: "codex", SourceID: "x1",
		StartedAt: now, UpdatedAt: now,
		Messages: []model.Message{{ID: "m", Role: model.RoleUser, Text: "hi", Timestamp: now}},
	}
	if err := ix.Upsert(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if need, err := ix.WantsRebuild(ctx); err != nil || !need {
		t.Fatalf("legacy-populated index wantsRebuild=%v err=%v", need, err)
	}

	// after marking, it's up to date
	if err := ix.MarkBuilt(ctx); err != nil {
		t.Fatal(err)
	}
	if need, err := ix.WantsRebuild(ctx); err != nil || need {
		t.Fatalf("marked index wantsRebuild=%v err=%v", need, err)
	}

	// reset wipes both sessions and fts rows
	if err := ix.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	hits, _ := ix.Search(ctx, ports.Query{Text: "hi"})
	if len(hits) != 0 {
		t.Errorf("Reset left %d rows behind", len(hits))
	}
}

// Two key properties of the default sort:
//  1. Newest session shows up first, even if an older session has more
//     matches (the original 'tape search codex' UX bug).
//  2. A single chatty session can't consume the whole result page;
//     SQL caps each session at maxHitsPerSession.
func TestSearchRecentSortAndPerSessionCap(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()

	put := func(id, agent string, when time.Time, n int) {
		msgs := make([]model.Message, n)
		for i := 0; i < n; i++ {
			msgs[i] = model.Message{
				ID: fmt.Sprintf("m%d", i), Role: model.RoleAssistant,
				Text: "needle codex needle " + fmt.Sprintf("%d", i),
				// in-session order: older messages first
				Timestamp: when.Add(time.Duration(i) * time.Second),
			}
		}
		s := &model.Session{
			ID: agent + "/" + id, Agent: agent, SourceID: id,
			Title: id, StartedAt: when, UpdatedAt: when.Add(time.Duration(n) * time.Second),
			Messages: msgs,
		}
		if err := ix.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// older session crammed with matches; newer session with just one
	put("old", "claude-code", time.Now().Add(-72*time.Hour), 50)
	put("new", "cursor", time.Now().Add(-1*time.Hour), 1)

	hits, err := ix.Search(ctx, ports.Query{Text: "codex", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].SessionID != "cursor/new" {
		t.Errorf("default sort should put newest session first, got %q", hits[0].SessionID)
	}
	perSession := map[string]int{}
	for _, h := range hits {
		perSession[h.SessionID]++
	}
	if perSession["claude-code/old"] > maxHitsPerSession {
		t.Errorf("per-session cap broken: %d", perSession["claude-code/old"])
	}
	if perSession["cursor/new"] < 1 {
		t.Errorf("newest session must appear in default sort: %v", perSession)
	}

	// --sort relevance restores BM25 ordering — the chatty session usually wins
	hitsR, err := ix.Search(ctx, ports.Query{Text: "codex", Limit: 20, Sort: "relevance"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsR) == 0 || hitsR[0].SessionID != "claude-code/old" {
		t.Errorf("relevance sort should let the chatty session bubble up, got %v", hitsR[0])
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
