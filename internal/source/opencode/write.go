package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

// Write materializes a session as fresh rows in the opencode SQLite
// database so the next launch of `opencode` (or its mimocode fork)
// lists the restored conversation in its TUI session picker.
//
// Returns ports.ErrNativeUnsupported when the target DB doesn't exist
// or doesn't expose the Drizzle session/message/part schema we read in
// Load — the restore CLI catches that sentinel and falls back to memory
// instead of writing into a database we don't understand. We never
// CREATE the schema ourselves: a user who never ran opencode shouldn't
// suddenly have a tape-generated DB the agent might refuse to open.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	if _, err := os.Stat(s.dbPath); err != nil {
		return ports.WriteResult{}, fmt.Errorf("%w: %s db not found at %s (install %s first, or use --strategy memory)",
			ports.ErrNativeUnsupported, s.agent, s.dbPath, s.agent)
	}
	db, err := sql.Open("sqlite", "file:"+s.dbPath+"?mode=rwc&_pragma=busy_timeout(3000)")
	if err != nil {
		return ports.WriteResult{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := requireSchema(ctx, db, s.agent); err != nil {
		return ports.WriteResult{}, err
	}

	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	// Tag the title so the user can spot it in the picker.
	title = "[tape] " + title

	projectID, err := ensureProject(ctx, db, cwd)
	if err != nil {
		return ports.WriteResult{}, err
	}

	now := time.Now().UTC()
	sid := "ses_" + scan.UUIDv4()
	slug := safeSlug(cwd)
	version := firstSessionVersion(ctx, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ports.WriteResult{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
		 VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?)`,
		sid, projectID, slug, cwd, title, version, now.UnixMilli(), now.UnixMilli()); err != nil {
		return ports.WriteResult{}, fmt.Errorf("insert session: %w", err)
	}

	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		ts := m.Timestamp
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Millisecond)
		}
		msgID := "msg_" + scan.UUIDv4()
		role := "user"
		if m.Role == model.RoleAssistant {
			role = "assistant"
		}
		msgData, _ := json.Marshal(map[string]any{
			"id":        msgID,
			"sessionID": sid,
			"role":      role,
			"time":      map[string]int64{"created": ts.UnixMilli()},
		})
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO message (id, session_id, time_created, time_updated, data)
			 VALUES (?, ?, ?, ?, ?)`,
			msgID, sid, ts.UnixMilli(), ts.UnixMilli(), string(msgData)); err != nil {
			return ports.WriteResult{}, fmt.Errorf("insert message: %w", err)
		}

		partID := "prt_" + scan.UUIDv4()
		partPayload, _ := json.Marshal(map[string]any{
			"type": "text", "text": m.Text,
		})
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			partID, msgID, sid, ts.UnixMilli(), ts.UnixMilli(), string(partPayload)); err != nil {
			return ports.WriteResult{}, fmt.Errorf("insert part: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ports.WriteResult{}, err
	}

	// Neither opencode nor mimocode publish a --resume <id> flag. The
	// TUI surfaces the new row at the top of the session list, so the
	// best we can do is cd back into the project and tell the user
	// what title to look for.
	bin := "opencode"
	if s.agent == "mimocode" {
		bin = "mimo"
	}
	hint := fmt.Sprintf("cd %s && %s  # restored as \"%s\" — pick it from the session list", cwd, bin, title)
	if s.agent == "mimocode" {
		// mimocode actually does publish --session <id>; use it.
		hint = fmt.Sprintf("cd %s && mimo --session %s", cwd, sid)
	}
	return ports.WriteResult{ResumeCommand: hint, TargetFile: s.dbPath}, nil
}

// requireSchema verifies the four tables we depend on are present. If
// the user is on a future opencode that renamed/split a table we'd
// rather bail with ErrNativeUnsupported than spray malformed rows.
func requireSchema(ctx context.Context, db *sql.DB, agent string) error {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master
		  WHERE type = 'table' AND name IN ('project','session','message','part')`)
	if err != nil {
		return fmt.Errorf("%w: %s schema check failed: %v", ports.ErrNativeUnsupported, agent, err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			got[n] = true
		}
	}
	for _, want := range []string{"project", "session", "message", "part"} {
		if !got[want] {
			return fmt.Errorf("%w: %s db missing table %q (schema mismatch)",
				ports.ErrNativeUnsupported, agent, want)
		}
	}
	return nil
}

// ensureProject finds-or-inserts the project row for cwd. opencode's
// session.project_id is nullable in practice but every real row carries
// one, so we mirror that — picking a stable-ish id derived from cwd so
// repeated tape restores into the same project reuse the same row.
func ensureProject(ctx context.Context, db *sql.DB, cwd string) (string, error) {
	var existing string
	err := db.QueryRowContext(ctx, `SELECT id FROM project WHERE directory = ? LIMIT 1`, cwd).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id := "prj_" + scan.UUIDv4()
	if _, err := db.ExecContext(ctx, `INSERT INTO project (id, directory) VALUES (?, ?)`, id, cwd); err != nil {
		return "", fmt.Errorf("insert project: %w", err)
	}
	return id, nil
}

// firstSessionVersion copies version off an existing session row so the
// new restored row stays consistent with the agent currently installed.
// Falls back to "0.0.0-tape" when the DB has no sessions yet (fresh
// install plus first-time restore — rare but possible).
func firstSessionVersion(ctx context.Context, db *sql.DB) string {
	var v string
	if err := db.QueryRowContext(ctx,
		`SELECT version FROM session WHERE version <> '' ORDER BY time_created DESC LIMIT 1`).
		Scan(&v); err == nil && v != "" {
		return v
	}
	return "0.0.0-tape"
}

// safeSlug derives a slug column value from cwd. opencode uses a free-
// form string here, mostly the project's basename, so we follow suit
// while staying ASCII-only so the TUI's list never gets a wide row.
func safeSlug(cwd string) string {
	base := filepath.Base(cwd)
	if base == "" || base == string(filepath.Separator) || base == "." {
		return "tape-restored"
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
