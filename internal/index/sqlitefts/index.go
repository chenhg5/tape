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
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// indexBuildVersion bumps whenever indexableText / the meta row layout /
// the tokenizer changes. Callers can compare against the value persisted
// in the props table (see WantsRebuild) and trigger a transparent
// rebuild from inside sync — users never have to think about indexes.
//
//	1 → initial release
//	2 → 2026-06-12: include ToolCall.Output + per-session @meta row
//	3 → 2026-06-12: zero per-message timestamps fall back to s.UpdatedAt
const indexBuildVersion = 3

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

CREATE TABLE IF NOT EXISTS props (
    key   TEXT PRIMARY KEY,
    value TEXT
);

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

// WantsRebuild reports whether the on-disk index was built by an older
// version of the indexer and therefore needs to be rebuilt to take
// advantage of newer fields/tokens. An empty/missing props row is treated
// as "needs rebuild" so brand-new installs and upgrades go through the
// same path.
func (ix *Index) WantsRebuild(ctx context.Context) (bool, error) {
	var raw string
	err := ix.db.QueryRowContext(ctx, `SELECT value FROM props WHERE key = 'build_version'`).Scan(&raw)
	if err != nil {
		// missing row → fresh DB, but also returns ErrNoRows; either way
		// we want a rebuild iff sessions table is non-empty (a brand-new
		// install has no sessions yet, so there is nothing to rebuild).
		var n int
		if cerr := ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); cerr != nil {
			return false, cerr
		}
		return n > 0, nil
	}
	v, _ := strconv.Atoi(raw)
	return v < indexBuildVersion, nil
}

// MarkBuilt stamps the current indexer version into the database. Call
// after a successful rebuild so subsequent runs see an up-to-date marker.
func (ix *Index) MarkBuilt(ctx context.Context) error {
	_, err := ix.db.ExecContext(ctx,
		`INSERT INTO props (key, value) VALUES ('build_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		strconv.Itoa(indexBuildVersion))
	return err
}

// SessionCount returns how many sessions are currently indexed. Used by
// the self-heal path in tape sync to compare against the archive.
func (ix *Index) SessionCount(ctx context.Context) (int, error) {
	var n int
	err := ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}

// Reset truncates every indexed row so the caller can fill it from
// scratch. Used by transparent rebuilds inside tape sync.
func (ix *Index) Reset(ctx context.Context) error {
	for _, q := range []string{`DELETE FROM fts_messages`, `DELETE FROM sessions`} {
		if _, err := ix.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

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
	// One synthetic "@meta" row per session so that queries like `search
	// codex`, `search auth-migration`, or a project name hit the session
	// even when no message body uses those words. Indexed text is the
	// agent name, title, project slug and cwd; the displayed snippet is a
	// short summary line.
	metaText := strings.TrimSpace(strings.Join([]string{
		s.Agent, s.Title, model.ProjectSlug(s.CWD), filepath.Base(s.CWD),
	}, " "))
	if metaText != "" {
		toks := strings.Join(Tokenize(metaText), " ")
		display := strings.TrimSpace(s.Title)
		if display == "" {
			display = s.CWD
		}
		display = s.Agent + " · " + display
		if _, err := stmt.ExecContext(ctx, toks, display, s.ID, "@meta", "meta", s.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
	}
	for _, m := range s.Messages {
		text := indexableText(m)
		if text == "" {
			continue
		}
		toks := strings.Join(Tokenize(text), " ")
		if toks == "" {
			continue
		}
		// Some adapters (Cursor in particular) don't keep per-message
		// timestamps in the source. Fall back to the session's
		// updated_at so every hit can render a meaningful "3h ago" and
		// the recent sort still has something to order by.
		ts := m.Timestamp
		if ts.IsZero() {
			ts = s.UpdatedAt
		}
		if _, err := stmt.ExecContext(ctx, toks, text, s.ID, m.ID, string(m.Role), ts.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// maxHitsPerSession caps how many matching messages the recent-sort
// path returns per conversation, so one long session can't shut out
// fresher ones from the result page. The UI may further trim this down
// for display. The relevance path keeps SQLite FTS5's native BM25 order
// and does not apply the cap (that's the point of sort=relevance).
const maxHitsPerSession = 5

func (ix *Index) Search(ctx context.Context, q ports.Query) ([]ports.Hit, error) {
	match := buildMatch(q.Text)
	if match == "" {
		return nil, fmt.Errorf("empty query")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	// Common WHERE clause shared by both sort paths.
	where := ` WHERE fts_messages MATCH ?`
	args := []any{match}
	if q.Agent != "" {
		where += ` AND s.agent = ?`
		args = append(args, q.Agent)
	}
	if q.Project != "" {
		where += ` AND (s.project LIKE ? OR instr(s.cwd, ?) > 0)`
		args = append(args, model.ProjectSlug(q.Project)+"%", q.Project)
	}
	if !q.Since.IsZero() {
		where += ` AND s.updated_at >= ?`
		args = append(args, q.Since.UnixMilli())
	}

	var sqlq string
	switch q.Sort {
	case "relevance":
		// Pure BM25 — long conversations that mention the term a lot win.
		sqlq = `
SELECT m.session_id, s.agent, s.title, s.project, m.message_id, m.role,
       m.text, m.ts
FROM fts_messages m
JOIN sessions s ON s.id = m.session_id` + where + `
ORDER BY rank LIMIT ? OFFSET ?`
		args = append(args, limit, q.Offset)
	default: // "recent"
		// Newest session first, with a per-session cap (window function)
		// so a chatty old session can't dominate the page.
		sqlq = `
SELECT session_id, agent, title, project, message_id, role, text, ts FROM (
  SELECT m.session_id, s.agent, s.title, s.project, m.message_id, m.role,
         m.text, m.ts, s.updated_at AS s_updated,
         ROW_NUMBER() OVER (PARTITION BY m.session_id ORDER BY m.ts ASC) AS rn
  FROM fts_messages m
  JOIN sessions s ON s.id = m.session_id` + where + `
) WHERE rn <= ?
ORDER BY s_updated DESC, ts ASC
LIMIT ? OFFSET ?`
		args = append(args, maxHitsPerSession, limit, q.Offset)
	}

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

// indexableText is the searchable text of a message: its body plus the
// name / input / output of every tool call attached to it. Tool outputs
// matter a lot in practice — for Codex-style agents the actual file
// contents, command results and most of the "evidence" the model used
// live there, not in the chat bubble. Source parsers already cap each
// field at scan.MaxToolIO so the index stays bounded.
func indexableText(m model.Message) string {
	parts := []string{m.Text}
	for _, tc := range m.ToolCalls {
		parts = append(parts, tc.Name, tc.Input, tc.Output)
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
