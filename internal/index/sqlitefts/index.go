// Package sqlitefts implements ports.Index with SQLite FTS5 (pure Go via
// modernc.org/sqlite). Tokenization happens in Go before insertion (see
// Tokenize), so the FTS table indexes a pre-tokenized shadow column while
// the original text is stored unindexed for display.
package sqlitefts

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    agent      TEXT NOT NULL,
    title      TEXT,
    project    TEXT,
    cwd        TEXT,
    started_at INTEGER,
    updated_at INTEGER,
    msg_count  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent, updated_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS fts_messages USING fts5(
    tokens,
    text       UNINDEXED,
    session_id UNINDEXED,
    message_id UNINDEXED,
    role       UNINDEXED,
    ts         UNINDEXED,
    tokenize = 'unicode61'
);
`

type Index struct {
	db *sql.DB
}

func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Index{db: db}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

func (ix *Index) Upsert(ctx context.Context, s *model.Session) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, agent, title, project, cwd, started_at, updated_at, msg_count)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   title=excluded.title, project=excluded.project, cwd=excluded.cwd,
		   started_at=excluded.started_at, updated_at=excluded.updated_at,
		   msg_count=excluded.msg_count`,
		s.ID, s.Agent, s.Title, model.ProjectSlug(s.CWD), s.CWD,
		s.StartedAt.UnixMilli(), s.UpdatedAt.UnixMilli(), len(s.Messages)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_messages WHERE session_id = ?`, s.ID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO fts_messages (tokens, text, session_id, message_id, role, ts) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range s.Messages {
		text := indexableText(m)
		if text == "" {
			continue
		}
		toks := strings.Join(Tokenize(text), " ")
		if toks == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, toks, text, s.ID, m.ID, string(m.Role), m.Timestamp.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (ix *Index) Search(ctx context.Context, q ports.Query) ([]ports.Hit, error) {
	match := buildMatch(q.Text)
	if match == "" {
		return nil, fmt.Errorf("empty query")
	}
	sqlq := `
SELECT m.session_id, s.agent, s.title, s.project, m.message_id, m.role, m.text, m.ts
FROM fts_messages m
JOIN sessions s ON s.id = m.session_id
WHERE fts_messages MATCH ?`
	args := []any{match}
	if q.Agent != "" {
		sqlq += ` AND s.agent = ?`
		args = append(args, q.Agent)
	}
	if q.Project != "" {
		sqlq += ` AND (s.project LIKE ? OR instr(s.cwd, ?) > 0)`
		args = append(args, model.ProjectSlug(q.Project)+"%", q.Project)
	}
	if !q.Since.IsZero() {
		sqlq += ` AND s.updated_at >= ?`
		args = append(args, q.Since.UnixMilli())
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	sqlq += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := ix.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []ports.Hit
	for rows.Next() {
		var h ports.Hit
		var text string
		var ts int64
		if err := rows.Scan(&h.SessionID, &h.Agent, &h.Title, &h.Project, &h.MessageID, &h.Role, &text, &ts); err != nil {
			return nil, err
		}
		h.Snippet = makeSnippet(text, q.Text)
		if ts > 0 {
			h.Timestamp = time.UnixMilli(ts).UTC()
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// indexableText is the searchable text of a message: its body plus tool
// call names/inputs (tool outputs are often huge and low-signal; the body
// of role=tool messages already carries the result text, capped upstream).
func indexableText(m model.Message) string {
	parts := []string{m.Text}
	for _, tc := range m.ToolCalls {
		parts = append(parts, tc.Name, tc.Input)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// makeSnippet cuts a window of the original text around the first query
// term occurrence. We do our own snippeting because FTS5's snippet() would
// return the bigram-tokenized shadow column, which is unreadable.
func makeSnippet(text, query string) string {
	const window = 240
	text = strings.Join(strings.Fields(text), " ") // collapse whitespace
	lower := strings.ToLower(text)

	pos := -1
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if i := strings.Index(lower, term); i >= 0 && (pos == -1 || i < pos) {
			pos = i
		}
	}
	if pos == -1 {
		pos = 0
	}
	start := pos - window/3
	if start < 0 {
		start = 0
	}
	// don't cut runes in half
	for start > 0 && !isRuneStart(text[start]) {
		start--
	}
	end := start + window
	if end >= len(text) {
		end = len(text)
	} else {
		for end > start && !isRuneStart(text[end]) {
			end--
		}
	}
	snip := text[start:end]
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(text) {
		snip += "…"
	}
	return snip
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
