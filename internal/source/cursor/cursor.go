// Package cursor reads Cursor CLI sessions from
// ~/.cursor/chats/<workspace-md5>/<session-uuid>/store.db.
//
// store.db is a SQLite content store:
//   - meta(key,value): key "0" holds hex-encoded JSON
//     {agentId, name, mode, createdAt, latestRootBlobId, lastUsedModel}
//   - blobs(id,data): content-addressed blobs. The root blob is a protobuf
//     whose repeated field 1 lists child blob ids (32-byte hashes) in
//     message order; each child that starts with '{' is a JSON message
//     {role, content} where content is a string or typed parts
//     (text / reasoning / tool-call / tool-result).
package cursor

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

const agentName = "cursor"

type Source struct {
	dir string // ~/.cursor/chats
}

func New(home string) *Source {
	return &Source{dir: filepath.Join(home, ".cursor", "chats")}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.dir); err != nil {
		return false, "", nil
	}
	return true, s.dir, nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*", "*", "store.db"))
	if err != nil {
		return nil, err
	}
	var refs []ports.SessionRef
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			continue
		}
		if !since.IsZero() && st.ModTime().Before(since) {
			continue
		}
		refs = append(refs, ports.SessionRef{
			Agent:     agentName,
			SourceID:  filepath.Base(filepath.Dir(f)), // session uuid directory
			Files:     []string{f},
			UpdatedAt: st.ModTime().UTC(),
		})
	}
	return refs, nil
}

type storeMeta struct {
	AgentID          string    `json:"agentId"`
	Name             string    `json:"name"`
	Mode             string    `json:"mode"`
	CreatedAt        flexInt64 `json:"createdAt"` // epoch millis; number or string depending on version
	LatestRootBlobID string    `json:"latestRootBlobId"`
	LastUsedModel    string    `json:"lastUsedModel"`
}

// flexInt64 tolerates both 1781256145540 and "1781256145540".
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil // fail-soft: timestamp stays zero
	}
	*f = flexInt64(v)
	return nil
}

type blobMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`   // tool-call
	Result     json.RawMessage `json:"result"` // tool-result
	Output     json.RawMessage `json:"output"`
}

var workspaceRe = regexp.MustCompile(`Workspace Path: ([^\n]+)`)

func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	db, err := sql.Open("sqlite", "file:"+ref.Files[0]+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	m, err := readMeta(ctx, db)
	if err != nil {
		return nil, err
	}
	sess := &model.Session{
		ID:       agentName + "/" + ref.SourceID,
		Agent:    agentName,
		SourceID: ref.SourceID,
		Title:    m.Name,
		Model:    m.LastUsedModel,
		Meta:     map[string]string{},
	}
	if m.Mode != "" {
		sess.Meta["mode"] = m.Mode
	}
	if m.CreatedAt > 0 {
		sess.StartedAt = time.UnixMilli(int64(m.CreatedAt)).UTC()
	}

	ids, err := rootChildren(ctx, db, m.LatestRootBlobID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		var data []byte
		if err := db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, id).Scan(&data); err != nil {
			continue // missing blob: fail-soft
		}
		if len(data) == 0 || data[0] != '{' {
			continue // binary blobs (checkpoints etc.) are not messages
		}
		var bm blobMessage
		if err := json.Unmarshal(data, &bm); err != nil {
			continue
		}
		if bm.Role == "system" {
			continue // vendor system prompt; lives on in the raw copy
		}
		msg, ok := parseMessage(bm, data)
		if !ok {
			continue
		}
		sess.Messages = append(sess.Messages, msg)
		if sess.CWD == "" && bm.Role == "user" {
			if m := workspaceRe.FindStringSubmatch(msg.Text); m != nil {
				sess.CWD = strings.TrimSpace(m[1])
			}
		}
	}
	if len(sess.Messages) == 0 {
		return nil, nil
	}
	sess.UpdatedAt = ref.UpdatedAt
	if sess.Title == "" {
		sess.Title = scan.FirstLine(firstUserText(sess), 80)
	}
	return sess, nil
}

func readMeta(ctx context.Context, db *sql.DB) (storeMeta, error) {
	var m storeMeta
	var hexVal string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = '0'`).Scan(&hexVal); err != nil {
		return m, fmt.Errorf("read meta: %w", err)
	}
	raw, err := hex.DecodeString(hexVal)
	if err != nil {
		return m, fmt.Errorf("decode meta hex: %w", err)
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

// rootChildren parses the root blob: protobuf repeated field 1, each entry a
// 32-byte blob id. Wire format per entry: tag 0x0a, varint length, payload.
func rootChildren(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	var data []byte
	if err := db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, rootID).Scan(&data); err != nil {
		return nil, fmt.Errorf("root blob %s: %w", rootID, err)
	}
	var ids []string
	for i := 0; i < len(data); {
		if data[i] != 0x0a {
			break // unknown field: stop, message ids always come first
		}
		i++
		ln, n := varint(data[i:])
		if n == 0 || i+n+int(ln) > len(data) {
			break
		}
		i += n
		ids = append(ids, hex.EncodeToString(data[i:i+int(ln)]))
		i += int(ln)
	}
	return ids, nil
}

func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func parseMessage(bm blobMessage, raw []byte) (model.Message, bool) {
	msg := model.Message{
		Role: normalizeRole(bm.Role),
		Raw:  json.RawMessage(append([]byte(nil), raw...)),
	}
	var text string
	if err := json.Unmarshal(bm.Content, &text); err == nil {
		msg.Text = text
	} else {
		var parts []contentPart
		if err := json.Unmarshal(bm.Content, &parts); err != nil {
			return model.Message{}, false
		}
		var texts []string
		for _, p := range parts {
			switch p.Type {
			case "text":
				texts = append(texts, p.Text)
			case "tool-call":
				msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
					Name:  p.ToolName,
					Input: scan.Truncate(string(p.Args), scan.MaxToolIO),
				})
			case "tool-result":
				out := p.Result
				if len(out) == 0 {
					out = p.Output
				}
				texts = append(texts, scan.Truncate(jsonText(out), scan.MaxToolIO))
			// "reasoning" parts are model-internal; skipped (raw keeps them)
			}
		}
		msg.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	}
	if msg.Text == "" && len(msg.ToolCalls) == 0 {
		return model.Message{}, false
	}
	return msg, true
}

func normalizeRole(r string) model.Role {
	switch r {
	case "user":
		return model.RoleUser
	case "assistant":
		return model.RoleAssistant
	case "tool":
		return model.RoleTool
	default:
		return model.Role(r)
	}
}

// jsonText renders a tool result, which may be a JSON string, an object, or
// a nested content list, into plain text.
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

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			// strip the harness envelope around the actual query
			if i := strings.Index(m.Text, "<user_query>"); i >= 0 {
				t := m.Text[i+len("<user_query>"):]
				if j := strings.Index(t, "</user_query>"); j >= 0 {
					t = t[:j]
				}
				return strings.TrimSpace(t)
			}
			return m.Text
		}
	}
	return ""
}
