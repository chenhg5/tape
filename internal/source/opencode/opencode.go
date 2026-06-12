// Package opencode reads sst/opencode sessions from
// ~/.local/share/opencode/opencode.db (Linux/macOS) — a SQLite database
// managed by Drizzle ORM. OpenCode's older JSON file layout is no longer
// authoritative; the SQLite store is the source of truth as of v1.x.
//
// Schema (per packages/opencode/src/session/session.sql.ts):
//
//	project(id PK, ...)
//	session(id PK, project_id, parent_id, slug, directory, title,
//	        version, time_created, time_updated, ...)
//	message(id PK, session_id, time_created, time_updated,
//	        data JSON: {id, sessionID, role:"user"|"assistant",
//	                     time:{created,completed?}, modelID?, providerID?,
//	                     path?:{cwd,root}, model?:{providerID,modelID}, ...})
//	part(id PK, message_id, session_id, time_created, time_updated,
//	     data JSON: discriminated union on `type`:
//	       text       {type:"text",      text}
//	       reasoning  {type:"reasoning", text}
//	       tool       {type:"tool",      tool, callID,
//	                   state:{status, input, output, error, ...}}
//	       file/snapshot/patch/step_start/step_finish/agent/retry/compaction
//
// We open the database read-only, list every session as a SessionRef, and
// rebuild model.Session by joining `message` with its parts ordered by
// `time_created, id`. Subagent sessions (parent_id != NULL) are skipped
// in the main listing — opencode itself only surfaces top-level sessions
// in the TUI, and we follow the same convention.
package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

const agentName = "opencode"

// Source reads opencode sessions from one of the platform-canonical
// data directories. We resolve the first matching path at construction
// time — opencode itself does the same XDG resolution.
type Source struct {
	dbPath string
}

func New(home string) *Source {
	return &Source{dbPath: resolveDBPath(home)}
}

// resolveDBPath honors $XDG_DATA_HOME first (which opencode respects),
// then falls back to the platform default. We don't probe for the file
// here — Detect does that — so an empty home still produces a stable
// path we can inspect.
func resolveDBPath(home string) string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "opencode", "opencode.db")
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	st, err := os.Stat(s.dbPath)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return false, "", nil
	}
	return true, filepath.Dir(s.dbPath), nil
}

// openDB opens the database read-only with a short busy timeout so a
// running opencode TUI doesn't make us hang or fail on locked pages.
// The `immutable=1` hint also lets sqlite skip WAL recovery — which we
// don't need anyway because we never write.
func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialize: modernc.org/sqlite handles 1 reader best
	return db, nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	if _, err := os.Stat(s.dbPath); err != nil {
		return nil, nil
	}
	db, err := openDB(s.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Top-level sessions only (parent_id IS NULL). Each session lives in
	// exactly one row, so file mtime is meaningless here; we use
	// time_updated from the row itself.
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(time_updated, time_created)
		   FROM session
		  WHERE parent_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []ports.SessionRef
	for rows.Next() {
		var id string
		var updated int64
		if err := rows.Scan(&id, &updated); err != nil {
			continue
		}
		updatedAt := epochToTime(updated)
		if !since.IsZero() && updatedAt.Before(since) {
			continue
		}
		refs = append(refs, ports.SessionRef{
			Agent:     agentName,
			SourceID:  id,
			Files:     []string{s.dbPath},
			UpdatedAt: updatedAt,
		})
	}
	return refs, rows.Err()
}

// epochToTime accepts opencode's epoch values, which may be in seconds
// (older rows), milliseconds (current), or microseconds (a few exporters).
// We pick the magnitude that lands in a reasonable century.
func epochToTime(v int64) time.Time {
	switch {
	case v == 0:
		return time.Time{}
	case v >= 1_000_000_000_000_000: // microseconds
		return time.UnixMicro(v).UTC()
	case v >= 1_000_000_000_000: // milliseconds
		return time.UnixMilli(v).UTC()
	default: // seconds
		return time.Unix(v, 0).UTC()
	}
}

// sessionRow mirrors the columns we read out of `session`.
type sessionRow struct {
	ID          string
	Slug        string
	Directory   string
	Title       string
	Version     string
	TimeCreated int64
	TimeUpdated int64
}

// messageRow + msgData together capture one row of `message`.
type messageRow struct {
	ID          string
	TimeCreated int64
	Data        msgData
}

type msgData struct {
	Role       string `json:"role"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Model      *struct {
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	} `json:"model"`
	Path *struct {
		CWD  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	Time *struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// partData is the discriminated-union payload of `part.data`. We keep
// only the fields we render; everything else stays in the raw row.
type partData struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	Tool   string          `json:"tool"`
	CallID string          `json:"callID"`
	State  *toolState      `json:"state"`
	Mime   string          `json:"mime"`
	Source json.RawMessage `json:"source"`
}

type toolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  string          `json:"error"`
}

func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	db, err := openDB(s.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var sr sessionRow
	err = db.QueryRowContext(ctx,
		`SELECT id, slug, directory, title, version, time_created, COALESCE(time_updated, time_created)
		   FROM session WHERE id = ?`, ref.SourceID).
		Scan(&sr.ID, &sr.Slug, &sr.Directory, &sr.Title, &sr.Version, &sr.TimeCreated, &sr.TimeUpdated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sess := &model.Session{
		ID:        agentName + "/" + sr.ID,
		Agent:     agentName,
		SourceID:  sr.ID,
		Title:     sr.Title,
		CWD:       sr.Directory,
		StartedAt: epochToTime(sr.TimeCreated),
		UpdatedAt: epochToTime(sr.TimeUpdated),
		Meta:      map[string]string{},
	}
	if sr.Version != "" {
		sess.Meta["cli_version"] = sr.Version
	}
	if sr.Slug != "" {
		sess.Meta["slug"] = sr.Slug
	}

	messages, err := loadMessages(ctx, db, sr.ID)
	if err != nil {
		return nil, err
	}
	parts, err := loadParts(ctx, db, sr.ID)
	if err != nil {
		return nil, err
	}

	sort.Slice(messages, func(i, j int) bool { return messages[i].TimeCreated < messages[j].TimeCreated })
	for _, mr := range messages {
		ps := parts[mr.ID]
		text, tools := flattenParts(ps)
		if text == "" && len(tools) == 0 {
			continue // empty turn — skip, the raw row is still archived
		}

		role := normalizeRole(mr.Data.Role)
		if role == "" {
			continue
		}
		ts := epochToTime(mr.TimeCreated)
		if mr.Data.Time != nil && mr.Data.Time.Created > 0 {
			ts = epochToTime(mr.Data.Time.Created)
		}

		raw, _ := json.Marshal(map[string]any{
			"message": mr.Data, "parts": partRaws(ps),
		})
		msg := model.Message{
			ID:        mr.ID,
			Role:      role,
			Text:      text,
			ToolCalls: tools,
			Timestamp: ts,
			Raw:       raw,
		}
		// First assistant message that carries a model gives us the
		// session model; opencode lets the user switch mid-session, so
		// we keep the first one for stability (matches what other
		// adapters expose).
		if sess.Model == "" {
			if mr.Data.Model != nil && mr.Data.Model.ModelID != "" {
				sess.Model = mr.Data.Model.ModelID
			} else if mr.Data.ModelID != "" {
				sess.Model = mr.Data.ModelID
			}
		}
		// Cover the case where session.directory is empty but the
		// assistant's path.cwd has it.
		if sess.CWD == "" && mr.Data.Path != nil && mr.Data.Path.CWD != "" {
			sess.CWD = mr.Data.Path.CWD
		}
		sess.Messages = append(sess.Messages, msg)
	}
	if len(sess.Messages) == 0 {
		return nil, nil
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = sess.Messages[len(sess.Messages)-1].Timestamp
	}
	if sess.Title == "" {
		sess.Title = scan.FirstLine(firstUserText(sess), 80)
	}
	return sess, nil
}

func loadMessages(ctx context.Context, db *sql.DB, sessionID string) ([]messageRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, time_created, data
		   FROM message
		  WHERE session_id = ?
		  ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []messageRow
	for rows.Next() {
		var mr messageRow
		var data []byte
		if err := rows.Scan(&mr.ID, &mr.TimeCreated, &data); err != nil {
			continue
		}
		_ = json.Unmarshal(data, &mr.Data) // fail-soft
		out = append(out, mr)
	}
	return out, rows.Err()
}

// loadParts pulls every part for a session in one round-trip and groups
// them by message_id. Parts within a message are ordered by time, then
// id, matching opencode's own UI ordering.
func loadParts(ctx context.Context, db *sql.DB, sessionID string) (map[string][]partData, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT message_id, data
		   FROM part
		  WHERE session_id = ?
		  ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := map[string][]partData{}
	for rows.Next() {
		var mid string
		var data []byte
		if err := rows.Scan(&mid, &data); err != nil {
			continue
		}
		var pd partData
		if err := json.Unmarshal(data, &pd); err != nil {
			continue
		}
		groups[mid] = append(groups[mid], pd)
	}
	return groups, rows.Err()
}

// flattenParts collapses a part list into a single text body + tool-call
// list, the shape the rest of tape expects. We surface:
//
//   - text parts: concatenated verbatim
//   - reasoning parts: prefixed with "[reasoning]" so they're visible in
//     search but distinguishable from user-visible prose
//   - tool parts: one ToolCall per part, carrying input + output if the
//     state is "completed"; pending/running tools surface their input only
//
// step_start / step_finish / snapshot / patch / agent / retry / compaction
// are bookkeeping — kept in the raw row, dropped from the dialogue.
func flattenParts(parts []partData) (string, []model.ToolCall) {
	var texts []string
	var tools []model.ToolCall
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		case "reasoning":
			if p.Text != "" {
				texts = append(texts, "[reasoning] "+p.Text)
			}
		case "tool":
			tc := model.ToolCall{Name: p.Tool}
			if p.State != nil {
				tc.Input = scan.Truncate(jsonText(p.State.Input), scan.MaxToolIO)
				if p.State.Status == "completed" {
					tc.Output = scan.Truncate(jsonText(p.State.Output), scan.MaxToolIO)
				} else if p.State.Status == "error" && p.State.Error != "" {
					tc.Output = scan.Truncate("[error] "+p.State.Error, scan.MaxToolIO)
				}
			}
			tools = append(tools, tc)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), tools
}

func normalizeRole(role string) model.Role {
	switch strings.ToLower(role) {
	case "user":
		return model.RoleUser
	case "assistant":
		return model.RoleAssistant
	case "tool":
		return model.RoleTool
	default:
		return ""
	}
}

func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// partRaws gathers raw bytes for the archive layer without bringing the
// huge tool I/O into the IR — we keep the structured payload, the raw
// db file copy stays the source of truth for full fidelity.
func partRaws(parts []partData) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		b, _ := json.Marshal(p)
		out = append(out, b)
	}
	return out
}

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}
