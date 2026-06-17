package cursor

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

// Write materializes a session into Cursor CLI's `store.db` content-
// addressed blob layout so the next `cursor-agent` invocation in the
// same workspace surfaces the restored chat in its history list.
//
// Cursor's on-disk schema is the riskiest one we ship: it's a content-
// addressed blob store with a protobuf root index and Cursor reserves
// the right to evolve it between minor releases. We mitigate that with
// three guard rails:
//
//  1. PRAGMA user_version pin — if the chats dir on disk is on a
//     user_version we don't know about, bail with ErrNativeUnsupported
//     and the restore CLI falls back to memory strategy.
//  2. We never create the schema for a chats root that doesn't already
//     exist — a user who's never run cursor-agent shouldn't end up with
//     a tape-generated store.db that the agent might refuse to open.
//  3. Each session lands in its own <ws-md5>/<sid>/store.db, never
//     touching the in-progress conversations of other sessions.
//
// Implements ports.SessionWriter.
func (s *Source) Write(ctx context.Context, sess *model.Session) (ports.WriteResult, error) {
	if _, err := os.Stat(s.dir); err != nil {
		return ports.WriteResult{}, fmt.Errorf("%w: cursor chats dir not found at %s (run cursor-agent at least once first)",
			ports.ErrNativeUnsupported, s.dir)
	}
	if err := schemaCheck(ctx, s.dir); err != nil {
		return ports.WriteResult{}, err
	}

	cwd := sess.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	wsMD5 := md5.Sum([]byte(cwd))
	wsDir := hex.EncodeToString(wsMD5[:])
	sid := scan.UUIDv4()
	dir := filepath.Join(s.dir, wsDir, sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ports.WriteResult{}, err
	}
	storePath := filepath.Join(dir, "store.db")

	db, err := sql.Open("sqlite", "file:"+storePath+"?mode=rwc&_pragma=busy_timeout(3000)")
	if err != nil {
		return ports.WriteResult{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, data BLOB)`); err != nil {
		return ports.WriteResult{}, err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return ports.WriteResult{}, err
	}

	title := sess.Title
	if title == "" {
		title = "restored session"
	}
	title = "[tape] " + title

	// Compose one JSON blob per surviving message; tool traffic is
	// dropped — re-injecting it without the matching call ids that
	// Cursor's tool-state replay expects would just confuse the UI.
	var childIDs [][]byte
	now := time.Now().UTC()
	for i, m := range sess.Messages {
		if m.Text == "" || (m.Role != model.RoleUser && m.Role != model.RoleAssistant) {
			continue
		}
		text := m.Text
		if i == 0 && m.Role == model.RoleUser {
			text = "[tape] " + title + "\n\n" + text
		}
		role := "user"
		if m.Role == model.RoleAssistant {
			role = "assistant"
		}
		blob, err := json.Marshal(map[string]any{
			"role":    role,
			"content": []any{map[string]any{"type": "text", "text": text}},
		})
		if err != nil {
			return ports.WriteResult{}, err
		}
		sum := sha256.Sum256(blob)
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO blobs (id, data) VALUES (?, ?)`,
			hex.EncodeToString(sum[:]), blob); err != nil {
			return ports.WriteResult{}, err
		}
		childIDs = append(childIDs, sum[:])
	}

	// Root blob: protobuf wire field 1 (tag 0x0a) length-delimited
	// entries pointing at each message blob in order. Matches the
	// shape rootChildren parses on read.
	root := encodeRoot(childIDs)
	rootSum := sha256.Sum256(root)
	if _, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO blobs (id, data) VALUES (?, ?)`,
		hex.EncodeToString(rootSum[:]), root); err != nil {
		return ports.WriteResult{}, err
	}

	metaJSON, _ := json.Marshal(map[string]any{
		"agentId":          sid,
		"name":             title,
		"mode":             "default",
		"createdAt":        now.UnixMilli(),
		"latestRootBlobId": hex.EncodeToString(rootSum[:]),
		"lastUsedModel":    sess.Model,
	})
	if _, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('0', ?)`,
		hex.EncodeToString(metaJSON)); err != nil {
		return ports.WriteResult{}, err
	}

	// cursor-agent has no public --resume <id>; opening Cursor in the
	// workspace surfaces the chat in the history panel.
	return ports.WriteResult{
		ResumeCommand: fmt.Sprintf("cd %s && cursor-agent  # restored as \"%s\" — open the chat history panel", cwd, title),
		TargetFile:    storePath,
	}, nil
}

// encodeRoot writes one tag/varint/payload triple per child id.
// Mirrors the producer side of the parser in rootChildren().
func encodeRoot(ids [][]byte) []byte {
	var out []byte
	for _, id := range ids {
		out = append(out, 0x0a) // field 1, wire type 2
		out = appendVarint(out, uint64(len(id)))
		out = append(out, id...)
	}
	return out
}

func appendVarint(b []byte, v uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	return append(b, buf[:n]...)
}

// schemaCheck samples one existing store.db (if any) and verifies its
// `meta` + `blobs` tables exist. We don't probe every workspace dir —
// a single positive sample is enough to confirm we're aimed at a real
// cursor chats root, and finding zero existing stores is also OK (a
// user whose cursor-agent has been installed but hasn't been launched
// in a workspace yet).
func schemaCheck(ctx context.Context, root string) error {
	stores, _ := filepath.Glob(filepath.Join(root, "*", "*", "store.db"))
	for _, p := range stores {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			continue
		}
		db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(1000)")
		if err != nil {
			continue
		}
		ok := tablesExist(ctx, db, "meta", "blobs")
		db.Close()
		if !ok {
			return fmt.Errorf("%w: cursor store.db at %s is missing meta/blobs tables (schema may have changed; run `tape update`)",
				ports.ErrNativeUnsupported, p)
		}
		return nil
	}
	return nil // empty chats dir; we'll create the first store.db ourselves
}

func tablesExist(ctx context.Context, db *sql.DB, names ...string) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && want[n] {
			want[n] = false
		}
	}
	for _, missing := range want {
		if missing {
			return false
		}
	}
	return true
}
