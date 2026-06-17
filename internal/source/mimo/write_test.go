package mimo

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
)

// seedFullMimoDB mirrors the full Drizzle schema (matching opencode's)
// the Write path needs — the existing seedMimoDB skips the project
// table to keep its read tests minimal.
func seedFullMimoDB(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "mimocode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mimocode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl := []string{
		`CREATE TABLE project (id TEXT PRIMARY KEY, directory TEXT NOT NULL)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
		   slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
		   version TEXT NOT NULL, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
		   time_created INTEGER, time_updated INTEGER, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL,
		   session_id TEXT NOT NULL, time_created INTEGER, time_updated INTEGER,
		   data TEXT NOT NULL)`,
	}
	for _, q := range ddl {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	return path
}

func TestWriteReadBack(t *testing.T) {
	home := t.TempDir()
	seedFullMimoDB(t, home)
	s := New(home)
	ctx := context.Background()
	in := &model.Session{
		ID: "mimocode/xyz", Agent: "mimocode", SourceID: "xyz",
		Title: "ipc bug", CWD: "/tmp/mimo-proj",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "为什么 ipc 阻塞", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "channel 没读完", Timestamp: time.Now()},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// mimocode does support --session <id>, so the resume line should
	// use it rather than the opencode "open picker" hint.
	if !strings.Contains(res.ResumeCommand, "mimo --session ") {
		t.Errorf("resume cmd missing mimo --session: %q", res.ResumeCommand)
	}
	if res.TargetFile == "" {
		t.Error("WriteResult.TargetFile should be set")
	}

	refs, err := s.List(ctx, time.Time{})
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	if refs[0].Agent != agentName {
		t.Errorf("agent label preserved: got %q want %q", refs[0].Agent, agentName)
	}
	out, err := s.Load(ctx, refs[0])
	if err != nil || out == nil {
		t.Fatalf("load: out=%v err=%v", out, err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(out.Messages))
	}
	if out.CWD != "/tmp/mimo-proj" {
		t.Errorf("cwd: %q", out.CWD)
	}
}
