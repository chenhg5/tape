package mimo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedMimoDB writes a minimal mimocode.db with the OpenCode schema —
// one top-level session, one user message, one text part — at the
// canonical Linux/macOS path under the supplied home.
func seedMimoDB(t *testing.T, home string) string {
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
	t.Cleanup(func() { db.Close() })

	ddl := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, slug TEXT,
		   directory TEXT, title TEXT, version TEXT,
		   time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT,
		   time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
		   time_created INTEGER, time_updated INTEGER, data TEXT)`,
	}
	for _, s := range ddl {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.Exec(
		`INSERT INTO session(id, parent_id, slug, directory, title, version,
		   time_created, time_updated)
		   VALUES (?, NULL, 'demo', '/tmp/proj', 'mimo demo', '0.1.0', ?, ?)`,
		"ses_mimo_demo", ts, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO message(id, session_id, time_created, time_updated, data)
		   VALUES (?, ?, ?, ?, ?)`,
		"msg1", "ses_mimo_demo", ts, ts,
		fmt.Sprintf(`{"role":"user","time":{"created":%d}}`, ts)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO part(id, message_id, session_id, time_created, time_updated, data)
		   VALUES (?, ?, ?, ?, ?, ?)`,
		"p1", "msg1", "ses_mimo_demo", ts, ts,
		`{"type":"text","text":"小米 MiMo Code 怎么用？"}`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectListLoadRelabelsToMimocode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MIMOCODE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	dbPath := seedMimoDB(t, home)

	src := New(home)
	if found, root, _ := src.Detect(context.Background()); !found ||
		root != filepath.Dir(dbPath) {
		t.Fatalf("Detect: found=%v root=%s want %s", found, root, filepath.Dir(dbPath))
	}

	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs: %d, want 1", len(refs))
	}
	if refs[0].Agent != "mimocode" {
		t.Errorf("ref agent = %q, want mimocode (opencode relabel must take effect)", refs[0].Agent)
	}
	if refs[0].SourceID != "ses_mimo_demo" {
		t.Errorf("source id = %q", refs[0].SourceID)
	}

	sess, err := src.Load(context.Background(), refs[0])
	if err != nil || sess == nil {
		t.Fatalf("Load: %v session=%v", err, sess)
	}
	if sess.Agent != "mimocode" || sess.ID != "mimocode/ses_mimo_demo" {
		t.Errorf("session relabel wrong: agent=%s id=%s", sess.Agent, sess.ID)
	}
	if len(sess.Messages) != 1 || !strings.Contains(sess.Messages[0].Text, "小米") {
		t.Fatalf("message: %+v", sess.Messages)
	}
}

func TestMimoHomeEnvOverride(t *testing.T) {
	home := t.TempDir()
	override := t.TempDir()
	// Move the db into MIMOCODE_HOME/data/ rather than the default
	// ~/.local/share/... layout, and assert resolveDBPath finds it.
	dataDir := filepath.Join(override, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "mimocode.db")
	if err := os.WriteFile(dbPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MIMOCODE_HOME", override)
	t.Setenv("XDG_DATA_HOME", "")
	got := resolveDBPath(home)
	if got != dbPath {
		t.Errorf("MIMOCODE_HOME override ignored: got %q, want %q", got, dbPath)
	}
}

func TestCandidatePathsCoverDocumentedLayouts(t *testing.T) {
	t.Setenv("MIMOCODE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	paths := candidatePaths("/home/u")
	want := []string{
		"/home/u/.local/share/mimocode/mimocode.db",
		"/home/u/.local/share/mimocode/storage/mimocode.db",
	}
	for _, w := range want {
		found := false
		for _, p := range paths {
			if p == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing documented path %q in %v", w, paths)
		}
	}
}
