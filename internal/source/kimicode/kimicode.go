// Package kimicode reads Moonshot AI's Kimi Code CLI sessions from
// ~/.kimi-code/sessions/<workDirKey>/<sessionId>/. Each session ships
// a small state.json (title, working directory, timestamps) plus one
// or more agent event streams under agents/<agentId>/wire.jsonl —
// the JSON-RPC 2.0 wire-protocol log replayed verbatim on resume
// (kimi --session <id> / kimi -C).
//
// We only ingest the top-level agent (agents/main/wire.jsonl). The
// sibling agents/<subagentId>/ directories are subagent transcripts;
// kimi-code itself surfaces them inside the main turn via
// SubagentEvent records, so re-importing them here would double-count
// the work. This mirrors how opencode hides parent_id != NULL rows.
//
// Wire schema (per https://www.kimi.com/code/docs/en/kimi-code-cli/
// customization/wire-protocol.html, protocol 1.7+). Every line is a
// JSON-RPC envelope; the lines we care about look like:
//
//	{"jsonrpc":"2.0","method":"event","params":{
//	    "type":"TurnBegin",
//	    "payload":{"user_input":"…"|[ContentPart,…]}}}
//
//	{"jsonrpc":"2.0","method":"event","params":{
//	    "type":"ContentPart",
//	    "payload":{"type":"text","text":"…"}}}
//	  (also "think" / "image_url" / "audio_url" / "video_url" payloads)
//
//	{"jsonrpc":"2.0","method":"event","params":{
//	    "type":"ToolCall",
//	    "payload":{"type":"function","id":"…",
//	               "function":{"name":"…","arguments":"…"}}}}
//
//	{"jsonrpc":"2.0","method":"event","params":{
//	    "type":"ToolResult",
//	    "payload":{"tool_call_id":"…",
//	               "return_value":{"is_error":bool,
//	                               "output":"…"|[ContentPart,…]}}}}
//
// Everything else (TurnEnd, StepBegin/Retry/Interrupted, StatusUpdate,
// CompactionBegin/End, PlanDisplay, Hook*, Subagent*, BtwBegin/End,
// SteerInput, ApprovalRequest/Response, ToolCallRequest, etc.) is
// bookkeeping; we keep it in the raw row but never surface it.
package kimicode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/internal/scan"
)

const agentName = "kimi-code"

// Source reads kimi-code sessions from a single root directory. Like
// every other tape source the path is resolved at construction so
// callers can inspect a deterministic dbPath/dir even when nothing
// is installed yet.
type Source struct {
	root string // <root>/sessions/<workDirKey>/<sessionId>/...
}

func New(home string) *Source {
	return &Source{root: resolveRoot(home)}
}

// resolveRoot honors $KIMI_CODE_HOME first (the env var documented at
// kimi-code/configuration/data-locations.html), then falls back to the
// platform default. The default is the same on Linux, macOS and
// Windows because kimi-code is npm-packaged and Node uses os.homedir()
// uniformly.
func resolveRoot(home string) string {
	if h := os.Getenv("KIMI_CODE_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".kimi-code")
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	dir := filepath.Join(s.root, "sessions")
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false, "", nil
	}
	return true, s.root, nil
}

// indexEntry mirrors a row of session_index.jsonl. kimi-code writes
// one line per session as it's created; we prefer this fast path over
// walking the sessions tree, but we tolerate a missing or stale index
// (some session may be on disk before the index gains its line, and
// older builds didn't ship the index at all).
type indexEntry struct {
	SessionID  string `json:"sessionId"`
	SessionDir string `json:"sessionDir"`
	WorkDir    string `json:"workDir"`
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	dir := filepath.Join(s.root, "sessions")
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}

	seen := map[string]ports.SessionRef{} // sessionID → ref, dedupe with the fs walk

	// Fast path: the index file. Each line is {sessionId,sessionDir,workDir}.
	indexPath := filepath.Join(s.root, "session_index.jsonl")
	if b, err := os.ReadFile(indexPath); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e indexEntry
			if json.Unmarshal([]byte(line), &e) != nil || e.SessionID == "" {
				continue
			}
			sessDir := e.SessionDir
			if !filepath.IsAbs(sessDir) {
				sessDir = filepath.Join(s.root, sessDir)
			}
			ref, ok := s.refForSession(e.SessionID, sessDir)
			if !ok {
				continue
			}
			if !since.IsZero() && ref.UpdatedAt.Before(since) {
				continue
			}
			seen[e.SessionID] = ref
		}
	}

	// Slow path: walk sessions/<workDirKey>/<sessionId>/. We always
	// run it (even if the index loaded) so sessions written outside
	// of the index — concurrent runs, manual recovery — still show.
	entries, _ := os.ReadDir(dir)
	for _, wd := range entries {
		if !wd.IsDir() {
			continue
		}
		bucket := filepath.Join(dir, wd.Name())
		sessDirs, _ := os.ReadDir(bucket)
		for _, sd := range sessDirs {
			if !sd.IsDir() {
				continue
			}
			id := sd.Name()
			if _, ok := seen[id]; ok {
				continue
			}
			ref, ok := s.refForSession(id, filepath.Join(bucket, id))
			if !ok {
				continue
			}
			if !since.IsZero() && ref.UpdatedAt.Before(since) {
				continue
			}
			seen[id] = ref
		}
	}

	out := make([]ports.SessionRef, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	// Stable order so two consecutive list calls don't reshuffle the
	// archive view.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].SourceID < out[j].SourceID
	})
	return out, nil
}

// refForSession builds a SessionRef for sessions/<…>/<sessionId>/,
// using main/wire.jsonl mtime as the freshness signal (state.json is
// rewritten less often, and an in-progress session may not yet have
// a settled state.json). Returns ok=false if main/wire.jsonl is
// missing or empty — those are placeholder dirs kimi creates ahead
// of the first user input.
func (s *Source) refForSession(id, sessDir string) (ports.SessionRef, bool) {
	wire := filepath.Join(sessDir, "agents", "main", "wire.jsonl")
	st, err := os.Stat(wire)
	if err != nil || st.Size() == 0 {
		return ports.SessionRef{}, false
	}
	files := []string{wire}
	if state := filepath.Join(sessDir, "state.json"); fileExists(state) {
		files = append(files, state)
	}
	return ports.SessionRef{
		Agent:     agentName,
		SourceID:  id,
		Files:     files,
		UpdatedAt: st.ModTime().UTC(),
	}, true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// state mirrors the subset of state.json we read. kimi-code's exact
// field names aren't part of the published Wire schema and may evolve;
// every field is optional and we fall back to sane defaults when one
// is missing.
type state struct {
	Title     string `json:"title"`
	WorkDir   string `json:"workDir"`
	CWD       string `json:"cwd"`
	Directory string `json:"directory"`
	Model     string `json:"model"`
	CreatedAt any    `json:"createdAt"` // string (ISO) or number (ms / s) — we coerce
	UpdatedAt any    `json:"updatedAt"`
	// Some builds nest creation under a {created_at, updated_at} key.
	CreatedAtSnake any `json:"created_at"`
	UpdatedAtSnake any `json:"updated_at"`
}

// envelope mirrors one wire.jsonl line. We only need method+params;
// everything inside params.payload is decoded per-event-type lazily.
type envelope struct {
	Method string          `json:"method"`
	Params eventParams     `json:"params"`
	ID     json.RawMessage `json:"id"`
}

type eventParams struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// contentPart is the discriminated union used by ContentPart events
// and by user_input / ToolResult.output when they aren't plain strings.
type contentPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text"`     // type=text
	Think    string         `json:"think"`    // type=think
	ImageURL map[string]any `json:"image_url"`
	AudioURL map[string]any `json:"audio_url"`
	VideoURL map[string]any `json:"video_url"`
}

// flattenParts joins a content-part array into the body text we surface
// in IR. We add a leading "[think] " marker for ThinkPart so it stays
// searchable but distinguishable from the user-visible prose, and skip
// the URL parts entirely (they're decoration in the transcript — the
// archived raw line is still the source of truth for fidelity).
func flattenParts(parts []contentPart) string {
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				out = append(out, p.Text)
			}
		case "think":
			if p.Think != "" {
				out = append(out, "[think] "+p.Think)
			}
		case "image_url":
			out = append(out, "[image]")
		case "audio_url":
			out = append(out, "[audio]")
		case "video_url":
			out = append(out, "[video]")
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	if len(ref.Files) == 0 {
		return nil, nil
	}
	wirePath := ref.Files[0]
	sessDir := filepath.Dir(filepath.Dir(filepath.Dir(wirePath))) // strip agents/main/wire.jsonl

	st := readState(filepath.Join(sessDir, "state.json"))
	cwd := firstNonEmpty(st.WorkDir, st.CWD, st.Directory)

	body, err := os.ReadFile(wirePath)
	if err != nil {
		return nil, err
	}

	sess := &model.Session{
		ID:        agentName + "/" + ref.SourceID,
		Agent:     agentName,
		SourceID:  ref.SourceID,
		Title:     st.Title,
		CWD:       cwd,
		Model:     st.Model,
		StartedAt: coerceTime(st.CreatedAt, st.CreatedAtSnake),
		UpdatedAt: ref.UpdatedAt,
		Meta:      map[string]string{},
	}
	if up := coerceTime(st.UpdatedAt, st.UpdatedAtSnake); !up.IsZero() {
		sess.UpdatedAt = up
	}

	// Walk events left-to-right, accumulating one user message per
	// TurnBegin and one assistant message per turn body. Tool calls
	// surface alongside text on the assistant message; ToolResult
	// payloads are attached to the matching ToolCall by id.
	//
	// wire.jsonl carries no per-line timestamps (the protocol is
	// designed for live replay over stdin/stdout, not historical
	// replay), so every IR message inherits the session's mtime.
	// That's coarse but it keeps Session.UpdatedAt-based ordering
	// and search index timestamps consistent with the rest of tape.
	var (
		cur       *model.Message
		callsByID = map[string]int{} // tool_call_id → index in cur.ToolCalls
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(cur.Text)
		if cur.Text != "" || len(cur.ToolCalls) > 0 {
			sess.Messages = append(sess.Messages, *cur)
		}
		cur = nil
		callsByID = map[string]int{}
	}

	lines := splitLines(body)
	if sess.StartedAt.IsZero() && len(lines) > 0 {
		sess.StartedAt = sess.UpdatedAt
	}

	for _, raw := range lines {
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Method != "event" {
			continue // skip request/response lines — UI dialogues, not content
		}
		switch env.Params.Type {

		case "TurnBegin":
			flush()
			var p struct {
				UserInput json.RawMessage `json:"user_input"`
			}
			_ = json.Unmarshal(env.Params.Payload, &p)
			text := decodeUserInput(p.UserInput)
			user := model.Message{
				ID:        fmt.Sprintf("turn-%d-user", len(sess.Messages)+1),
				Role:      model.RoleUser,
				Text:      text,
				Timestamp: sess.UpdatedAt,
				Raw:       raw,
			}
			sess.Messages = append(sess.Messages, user)

		case "ContentPart":
			var p contentPart
			if json.Unmarshal(env.Params.Payload, &p) != nil {
				continue
			}
			t := flattenParts([]contentPart{p})
			if t == "" {
				continue
			}
			ensureAssistant(&cur, sess, raw)
			if cur.Text != "" {
				cur.Text += "\n"
			}
			cur.Text += t

		case "ToolCall":
			var p struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(env.Params.Payload, &p) != nil {
				continue
			}
			ensureAssistant(&cur, sess, raw)
			cur.ToolCalls = append(cur.ToolCalls, model.ToolCall{
				Name:  p.Function.Name,
				Input: scan.Truncate(p.Function.Arguments, scan.MaxToolIO),
			})
			callsByID[p.ID] = len(cur.ToolCalls) - 1

		case "ToolResult":
			var p struct {
				ToolCallID  string `json:"tool_call_id"`
				ReturnValue struct {
					IsError bool            `json:"is_error"`
					Output  json.RawMessage `json:"output"`
				} `json:"return_value"`
			}
			if json.Unmarshal(env.Params.Payload, &p) != nil {
				continue
			}
			idx, ok := callsByID[p.ToolCallID]
			if !ok || cur == nil || idx >= len(cur.ToolCalls) {
				continue
			}
			out := decodeToolOutput(p.ReturnValue.Output)
			if p.ReturnValue.IsError && out != "" {
				out = "[error] " + out
			}
			cur.ToolCalls[idx].Output = scan.Truncate(out, scan.MaxToolIO)

		default:
			// Bookkeeping events: TurnEnd, StepBegin, StatusUpdate,
			// CompactionBegin/End, PlanDisplay, SubagentEvent,
			// HookTriggered/Resolved, BtwBegin/End, SteerInput,
			// StepInterrupted, StepRetry, ApprovalResponse, …
			continue
		}
	}
	flush()

	if len(sess.Messages) == 0 {
		return nil, nil
	}
	if sess.Title == "" {
		sess.Title = scan.FirstLine(firstUserText(sess), 80)
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = sess.Messages[0].Timestamp
	}
	return sess, nil
}

// ensureAssistant lazily creates the current turn's assistant message.
// We delay creation until the first event with content so a turn the
// user cancelled before any output doesn't leave an empty bubble.
func ensureAssistant(cur **model.Message, sess *model.Session, raw []byte) {
	if *cur != nil {
		return
	}
	*cur = &model.Message{
		ID:        fmt.Sprintf("turn-%d-assistant", len(sess.Messages)+1),
		Role:      model.RoleAssistant,
		Timestamp: sess.UpdatedAt,
		Raw:       raw,
	}
}

// decodeUserInput unwraps user_input which can be either a plain string
// or a ContentPart[] (multimodal input). Empty user inputs are valid
// (e.g. /compact / /continue) — we just return "".
func decodeUserInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) == nil {
		return flattenParts(parts)
	}
	return ""
}

// decodeToolOutput mirrors decodeUserInput's logic for ToolResult.output.
func decodeToolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) == nil {
		return flattenParts(parts)
	}
	return string(raw)
}

// splitLines splits a JSONL body into trimmed non-empty lines. Cheaper
// than bufio.Scanner here because we already have the bytes in memory
// (a single wire.jsonl is < a few MB for normal sessions).
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				line := b[start:i]
				// trim trailing CR for Windows-written files
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				if len(line) > 0 {
					out = append(out, line)
				}
			}
			start = i + 1
		}
	}
	if start < len(b) {
		line := b[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			out = append(out, line)
		}
	}
	return out
}

func readState(p string) state {
	var st state
	b, err := os.ReadFile(p)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// coerceTime accepts either an RFC3339 string or a numeric epoch
// (seconds / ms / µs depending on the field), preferring camelCase
// then snake_case. Returns zero time if neither field carried a usable
// value — callers fall back to the wire mtime in that case.
func coerceTime(prefer, fallback any) time.Time {
	for _, v := range []any{prefer, fallback} {
		switch t := v.(type) {
		case string:
			if t == "" {
				continue
			}
			if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return ts.UTC()
			}
			if ts, err := time.Parse(time.RFC3339, t); err == nil {
				return ts.UTC()
			}
		case float64:
			n := int64(t)
			if n <= 0 {
				continue
			}
			switch {
			case n >= 1_000_000_000_000_000: // µs
				return time.UnixMicro(n).UTC()
			case n >= 1_000_000_000_000: // ms
				return time.UnixMilli(n).UTC()
			default: // s
				return time.Unix(n, 0).UTC()
			}
		}
	}
	return time.Time{}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func firstUserText(s *model.Session) string {
	for _, m := range s.Messages {
		if m.Role == model.RoleUser && m.Text != "" {
			return m.Text
		}
	}
	return ""
}
