// Package aider reads Aider chat history from `.aider.chat.history.md`.
//
// Aider stores every conversation in a single human-readable markdown
// file (default `~/.aider.chat.history.md`, plus an optional per-project
// file at `<cwd>/.aider.chat.history.md`). Each turn is prefixed:
//
//	# aider chat started at 2024-08-22 10:30:45  -- session header
//	#### user input lines (each line prefixed)
//	> tool output / aider system messages
//	plain text from the LLM (no prefix)
//
// We split the file at each `# aider chat started at ...` header — every
// such block becomes one tape session. Within a block we follow the
// same role-classification rules aider's own `split_chat_history_markdown`
// uses, so search and replay match what the user remembers seeing.
package aider

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

const agentName = "aider"

// Source discovers Aider history files. We always check the global
// location and, optionally, a list of seeded project directories.
// (Per-project histories are picked up automatically by `tape sync`
// from the user's current cwd, but the agent's main fingerprint is the
// global file in $HOME.)
type Source struct {
	home string
}

func New(home string) *Source { return &Source{home: home} }

func (s *Source) Name() string { return agentName }

// Detect succeeds when either the global ~/.aider.chat.history.md exists
// or a project history is found in the current working directory.
func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	for _, p := range s.candidates() {
		if _, err := os.Stat(p); err == nil {
			return true, s.home, nil
		}
	}
	return false, "", nil
}

// candidates returns the markdown files we know how to read. We avoid
// recursively scanning $HOME by enumerating the two standard locations
// only — anything else would be a privacy footgun (aider history can
// live next to private project files).
func (s *Source) candidates() []string {
	out := []string{
		filepath.Join(s.home, ".aider.chat.history.md"),
	}
	if wd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(wd, ".aider.chat.history.md"))
	}
	return out
}

// List returns one ref per session within each discovered history file.
// SourceID is "<file-stem>-<session-index>" so each session has a stable,
// human-readable id even though the underlying storage is one big file.
func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	var refs []ports.SessionRef
	for _, p := range s.candidates() {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			continue
		}
		if !since.IsZero() && st.ModTime().Before(since) {
			continue
		}
		blocks, err := splitFile(p)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			// Each session gets its own ref, but they all point at the
			// same underlying file (Files[0]). The archive layer keeps
			// one raw copy regardless of how many refs reference it.
			refs = append(refs, ports.SessionRef{
				Agent:     agentName,
				SourceID:  sessionID(p, b.startedAt, b.offset),
				Files:     []string{p},
				UpdatedAt: blockUpdatedAt(b, st.ModTime()),
			})
		}
	}
	return refs, nil
}

// block carries the raw markdown for one session plus its header time.
type block struct {
	startedAt time.Time
	offset    int    // byte offset in the file (for stable id hashing)
	body      string // raw markdown of this session block
}

func blockUpdatedAt(b block, fallback time.Time) time.Time {
	if !b.startedAt.IsZero() {
		return b.startedAt
	}
	return fallback
}

var sessionHeaderRe = regexp.MustCompile(`(?m)^# aider chat started at (.+)$`)

// splitFile slices the chat history at each "# aider chat started at"
// header. The first block (before any header) is dropped — that's just
// pre-session housekeeping aider prints on file creation.
func splitFile(path string) ([]block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	idx := sessionHeaderRe.FindAllSubmatchIndex(data, -1)
	if len(idx) == 0 {
		// No headers at all → treat whole file as a single session, with
		// the file mtime as the start time.
		st, _ := os.Stat(path)
		return []block{{startedAt: st.ModTime().UTC(), offset: 0, body: string(data)}}, nil
	}
	var out []block
	for i, m := range idx {
		start, end := m[0], len(data)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		tsRaw := strings.TrimSpace(string(data[m[2]:m[3]]))
		ts, _ := time.Parse("2006-01-02 15:04:05", tsRaw)
		out = append(out, block{startedAt: ts.UTC(), offset: start, body: string(data[start:end])})
	}
	return out, nil
}

// sessionID hashes (filename, startedAt, byte-offset) into a short stable
// id. Using a hash keeps it short and lets us treat per-project files
// and the global file independently without collisions.
func sessionID(path string, started time.Time, offset int) string {
	h := sha1.New()
	h.Write([]byte(filepath.Base(path)))
	h.Write([]byte(started.UTC().Format(time.RFC3339)))
	h.Write([]byte{byte(offset >> 16), byte(offset >> 8), byte(offset)})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Load parses one block back into a model.Session.
func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	blocks, err := splitFile(ref.Files[0])
	if err != nil {
		return nil, err
	}
	var b block
	found := false
	for _, candidate := range blocks {
		if sessionID(ref.Files[0], candidate.startedAt, candidate.offset) == ref.SourceID {
			b = candidate
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}

	sess := &model.Session{
		ID:        agentName + "/" + ref.SourceID,
		Agent:     agentName,
		SourceID:  ref.SourceID,
		StartedAt: b.startedAt,
		Meta:      map[string]string{"history_file": ref.Files[0]},
	}

	messages := splitMessages(b.body)
	for i, m := range messages {
		messages[i].ID = newMsgID(ref.SourceID, i)
		if !b.startedAt.IsZero() {
			messages[i].Timestamp = b.startedAt
		}
		messages[i].Raw = json.RawMessage(m.Raw) // pre-set in splitMessages
	}
	sess.Messages = messages
	if len(sess.Messages) == 0 {
		return nil, nil
	}
	sess.UpdatedAt = sess.Messages[len(sess.Messages)-1].Timestamp
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = ref.UpdatedAt
	}
	sess.Title = scan.FirstLine(firstUserText(sess), 80)
	return sess, nil
}

func newMsgID(sessionID string, i int) string {
	h := sha1.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{byte(i)})
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// splitMessages walks a session block line-by-line, following the same
// classification aider's own utils.py uses:
//
//   - `#### ` starts a user message (multi-line until a non-`####` line)
//   - `> ` lines accumulate as tool output
//   - anything else is assistant text
//
// We collapse runs of the same role into one message each — markdown
// readers expect blank lines between paragraphs, not between every line.
func splitMessages(body string) []model.Message {
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)

	var msgs []model.Message
	var role model.Role
	var buf []string
	flush := func() {
		if len(buf) == 0 {
			return
		}
		text := strings.TrimRight(strings.Join(buf, "\n"), "\n")
		if strings.TrimSpace(text) != "" {
			raw, _ := json.Marshal(map[string]string{
				"role": string(role), "text": text,
			})
			msgs = append(msgs, model.Message{Role: role, Text: text, Raw: raw})
		}
		buf = buf[:0]
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# aider chat started at"):
			// session header — only ever the first line, but be defensive
			continue
		case strings.HasPrefix(line, "#### "):
			if role != model.RoleUser {
				flush()
				role = model.RoleUser
			}
			buf = append(buf, strings.TrimPrefix(line, "#### "))
		case strings.HasPrefix(line, "> "):
			if role != model.RoleTool {
				flush()
				role = model.RoleTool
			}
			buf = append(buf, strings.TrimPrefix(line, "> "))
		case strings.TrimSpace(line) == "":
			// keep blank lines inside the current message body
			if role != "" {
				buf = append(buf, "")
			}
		default:
			if role != model.RoleAssistant {
				flush()
				role = model.RoleAssistant
			}
			buf = append(buf, line)
		}
	}
	flush()
	return msgs
}

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}
