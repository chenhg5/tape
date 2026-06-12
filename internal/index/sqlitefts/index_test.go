package sqlitefts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
