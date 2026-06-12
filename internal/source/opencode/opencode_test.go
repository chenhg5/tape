package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// refFor builds a SessionRef that points at the DB file but uses an
// arbitrary id — handy for targeting rows List() intentionally hides
// (subagents, empty sessions, etc.).
func refFor(src *Source, id string) ports.SessionRef {
	return ports.SessionRef{
		Agent:    agentName,
		SourceID: id,
		Files:    []string{src.dbPath},
	}
}

// seedDB builds a minimal-but-real opencode SQLite database matching the
// shape Drizzle creates in production. We hand-craft the schema (instead
// of importing opencode's TS) so the test stays hermetic and obvious.
func seedDB(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	// Schema (Drizzle's actual DDL, transcribed):
	exec(`CREATE TABLE project (
		id TEXT PRIMARY KEY,
		directory TEXT NOT NULL
	)`)
	exec(`CREATE TABLE session (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		parent_id TEXT,
		slug TEXT NOT NULL,
		directory TEXT NOT NULL,
		title TEXT NOT NULL,
		version TEXT NOT NULL,
		time_created INTEGER,
		time_updated INTEGER
	)`)
	exec(`CREATE TABLE message (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		time_created INTEGER,
		time_updated INTEGER,
		data TEXT NOT NULL
	)`)
	exec(`CREATE TABLE part (
		id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		time_created INTEGER,
		time_updated INTEGER,
		data TEXT NOT NULL
	)`)

	exec(`INSERT INTO project VALUES (?,?)`, "prj_root", "/root/code/demo")

	// Two sessions: a top-level conversation and a subagent child that
	// must be filtered out of the listing.
	exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?,?,?)`,
		"ses_top", "prj_root", nil, "demo", "/root/code/demo",
		"Refactor auth", "1.2.3", int64(1781256145540), int64(1781256999999))
	exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?,?,?)`,
		"ses_sub", "prj_root", "ses_top", "demo", "/root/code/demo",
		"explore", "1.2.3", int64(1781256200000), int64(1781256300000))

	// User message + assistant message with mixed parts.
	uData, _ := json.Marshal(map[string]any{
		"id": "msg_u1", "sessionID": "ses_top", "role": "user",
		"time": map[string]int64{"created": 1781256146000},
	})
	aData, _ := json.Marshal(map[string]any{
		"id": "msg_a1", "sessionID": "ses_top", "role": "assistant",
		"time":  map[string]int64{"created": 1781256148000},
		"model": map[string]string{"providerID": "anthropic", "modelID": "claude-fable-5"},
		"path":  map[string]string{"cwd": "/root/code/demo", "root": "/root/code/demo"},
	})

	exec(`INSERT INTO message VALUES (?,?,?,?,?)`,
		"msg_u1", "ses_top", int64(1781256146000), int64(1781256146000), string(uData))
	exec(`INSERT INTO message VALUES (?,?,?,?,?)`,
		"msg_a1", "ses_top", int64(1781256148000), int64(1781256148000), string(aData))

	parts := []struct {
		id, msg string
		t       int64
		data    map[string]any
	}{
		{"part_u_text", "msg_u1", 1781256146100,
			map[string]any{"type": "text", "text": "重构 auth.go"}},
		{"part_a_reason", "msg_a1", 1781256147000,
			map[string]any{"type": "reasoning", "text": "先读取现有实现"}},
		{"part_a_tool", "msg_a1", 1781256147500,
			map[string]any{
				"type": "tool", "tool": "read_file", "callID": "call_1",
				"state": map[string]any{
					"status": "completed",
					"input":  map[string]string{"path": "auth.go"},
					"output": "package auth\nfunc Login(...) {}",
				},
			}},
		{"part_a_text", "msg_a1", 1781256148500,
			map[string]any{"type": "text", "text": "已切换到 JWT"}},
		// step_finish should be ignored by flattenParts.
		{"part_a_step", "msg_a1", 1781256148800,
			map[string]any{"type": "step_finish", "cost": 0.001}},
	}
	for _, p := range parts {
		raw, _ := json.Marshal(p.data)
		exec(`INSERT INTO part VALUES (?,?,?,?,?,?)`,
			p.id, p.msg, "ses_top", p.t, p.t, string(raw))
	}
	return dbPath
}

func TestDetectAndList(t *testing.T) {
	home := t.TempDir()
	src := New(home)

	if found, _, _ := src.Detect(context.Background()); found {
		t.Fatal("Detect on empty home should be false")
	}
	seedDB(t, home)
	if found, _, _ := src.Detect(context.Background()); !found {
		t.Fatal("Detect after seed should be true")
	}

	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Subagent (parent_id != NULL) must be filtered out.
	if len(refs) != 1 {
		t.Fatalf("List: got %d refs, want 1 (subagent must be hidden)", len(refs))
	}
	if refs[0].SourceID != "ses_top" {
		t.Errorf("sourceID: %q", refs[0].SourceID)
	}
}

func TestLoadAssemblesSession(t *testing.T) {
	home := t.TempDir()
	seedDB(t, home)
	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})

	s, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("session is nil")
	}
	if s.Title != "Refactor auth" {
		t.Errorf("title: %q", s.Title)
	}
	if s.CWD != "/root/code/demo" {
		t.Errorf("cwd: %q", s.CWD)
	}
	if s.Model != "claude-fable-5" {
		t.Errorf("model: %q", s.Model)
	}
	if got := len(s.Messages); got != 2 {
		t.Fatalf("messages: got %d, want 2", got)
	}
	u := s.Messages[0]
	if u.Role != model.RoleUser || u.Text != "重构 auth.go" {
		t.Errorf("user msg: %+v", u)
	}
	a := s.Messages[1]
	if a.Role != model.RoleAssistant {
		t.Errorf("assistant role: %v", a.Role)
	}
	// reasoning surfaces but is tagged, regular text follows, step_finish dropped
	if !strings.Contains(a.Text, "[reasoning] 先读取现有实现") {
		t.Errorf("reasoning missing from assistant text:\n%s", a.Text)
	}
	if !strings.Contains(a.Text, "已切换到 JWT") {
		t.Errorf("assistant final text missing:\n%s", a.Text)
	}
	if len(a.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d, want 1", len(a.ToolCalls))
	}
	if a.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool name: %q", a.ToolCalls[0].Name)
	}
	if !strings.Contains(a.ToolCalls[0].Output, "package auth") {
		t.Errorf("tool output missing: %q", a.ToolCalls[0].Output)
	}
}

func TestLoadEmptySessionReturnsNil(t *testing.T) {
	home := t.TempDir()
	dbPath := seedDB(t, home)

	// Add an empty session: row exists, but no parts → flattened to nothing.
	db, _ := sql.Open("sqlite", dbPath)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?,?,?)`,
		"ses_empty", "prj_root", nil, "empty", "/root/code/demo",
		"", "1.2.3", int64(1781256000000), int64(1781256000000))
	data, _ := json.Marshal(map[string]any{
		"id": "msg_empty", "sessionID": "ses_empty", "role": "assistant",
	})
	_, _ = db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`,
		"msg_empty", "ses_empty", int64(1781256000000), int64(1781256000000), string(data))

	src := New(home)
	s, err := src.Load(context.Background(), refFor(src, "ses_empty"))
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Errorf("empty session should map to nil, got %+v", s)
	}
}

func TestLoadUnknownIDReturnsNil(t *testing.T) {
	home := t.TempDir()
	seedDB(t, home)
	src := New(home)
	s, err := src.Load(context.Background(), refFor(src, "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Errorf("missing session should be nil, got %+v", s)
	}
}

func TestXDGOverridesHomePath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-test-only")
	src := New("/home/anywhere")
	if got, want := src.dbPath, "/tmp/xdg-test-only/opencode/opencode.db"; got != want {
		t.Errorf("XDG override ignored: got %q, want %q", got, want)
	}
}
