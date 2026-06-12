package cursor

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
)

// buildStore writes a store.db replicating Cursor CLI's layout: hex-encoded
// JSON meta plus content-addressed blobs chained from a protobuf root.
func buildStore(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	msgs := [][]byte{
		[]byte(`{"role":"system","content":"vendor system prompt"}`),
		[]byte(`{"role":"user","content":[{"type":"text","text":"<user_info>Workspace Path: /root/code/demo\n</user_info><user_query>会话怎么备份</user_query>"}]}`),
		[]byte(`{"role":"assistant","content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"用 tape sync 即可。"},{"type":"tool-call","toolCallId":"t1","toolName":"Shell","args":{"command":"ls"}}]}`),
		[]byte(`{"role":"tool","content":[{"type":"tool-result","toolCallId":"t1","toolName":"Shell","result":"file1 file2"}]}`),
	}
	var root []byte
	for i, m := range msgs {
		id := make([]byte, 32)
		id[0] = byte(i + 1)
		if _, err := db.Exec(`INSERT INTO blobs (id, data) VALUES (?,?)`, hex.EncodeToString(id), m); err != nil {
			t.Fatal(err)
		}
		root = append(root, 0x0a, 32)
		root = append(root, id...)
	}
	rootID := make([]byte, 32)
	rootID[0] = 0xff
	if _, err := db.Exec(`INSERT INTO blobs (id, data) VALUES (?,?)`, hex.EncodeToString(rootID), root); err != nil {
		t.Fatal(err)
	}

	// createdAt deliberately a JSON number (real data), while flexInt64
	// also accepts the string form seen in other versions
	metaJSON := []byte(`{"agentId":"` + filepath.Base(dir) + `","name":"Backup chat","mode":"default","createdAt":1781256145540,"latestRootBlobId":"` + hex.EncodeToString(rootID) + `","lastUsedModel":"claude-fable-5"}`)
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('0', ?)`, hex.EncodeToString(metaJSON)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFixture(t *testing.T) {
	base := t.TempDir()
	buildStore(t, filepath.Join(base, "36e6f82a4f9ae16e16ce627c19e4b65f", "f1ff7b74-0a28-4dd4-a452-e01ae080eb20"))

	s := &Source{dir: base}
	ctx := context.Background()
	refs, err := s.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 ref, got %d", len(refs))
	}
	if refs[0].SourceID != "f1ff7b74-0a28-4dd4-a452-e01ae080eb20" {
		t.Errorf("sourceID = %q", refs[0].SourceID)
	}
	sess, err := s.Load(ctx, refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "Backup chat" || sess.Model != "claude-fable-5" {
		t.Errorf("title/model = %q/%q", sess.Title, sess.Model)
	}
	if sess.CWD != "/root/code/demo" {
		t.Errorf("cwd extracted from user_info = %q", sess.CWD)
	}
	// system skipped: user + assistant + tool
	if len(sess.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(sess.Messages))
	}
	a := sess.Messages[1]
	if a.Role != model.RoleAssistant || a.Text != "用 tape sync 即可。" {
		t.Errorf("assistant text = %q (reasoning must be excluded)", a.Text)
	}
	if len(a.ToolCalls) != 1 || a.ToolCalls[0].Name != "Shell" {
		t.Errorf("tool calls = %+v", a.ToolCalls)
	}
	if sess.Messages[2].Role != model.RoleTool || sess.Messages[2].Text != "file1 file2" {
		t.Errorf("tool msg = %+v", sess.Messages[2])
	}
	if sess.StartedAt.UnixMilli() != 1781256145540 {
		t.Errorf("startedAt = %v", sess.StartedAt)
	}
}

func TestFlexInt64(t *testing.T) {
	cases := map[string]int64{
		`1781256145540`:   1781256145540,
		`"1781256145540"`: 1781256145540,
		`null`:            0,
		`""`:              0,
		`"not-a-number"`:  0, // fail-soft
	}
	for in, want := range cases {
		var f flexInt64
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Errorf("flexInt64(%s): %v", in, err)
		}
		if int64(f) != want {
			t.Errorf("flexInt64(%s) = %d, want %d", in, f, want)
		}
	}
}

func TestVarint(t *testing.T) {
	if v, n := varint([]byte{0x20}); v != 32 || n != 1 {
		t.Errorf("single byte: %d %d", v, n)
	}
	if v, n := varint([]byte{0xac, 0x02}); v != 300 || n != 2 {
		t.Errorf("two bytes: %d %d", v, n)
	}
	if _, n := varint([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}); n != 0 {
		t.Errorf("unterminated varint must fail, n=%d", n)
	}
	if _, n := varint(nil); n != 0 {
		t.Errorf("empty input: n=%d", n)
	}
}

func TestJSONText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"a string"`, "a string"},
		{`{"k":"v"}`, `{"k":"v"}`},
		{``, ""},
	}
	for _, c := range cases {
		if got := jsonText(json.RawMessage(c.in)); got != c.want {
			t.Errorf("jsonText(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRole(t *testing.T) {
	if normalizeRole("tool") != model.RoleTool || normalizeRole("custom") != model.Role("custom") {
		t.Error("role mapping broken")
	}
}

func TestFirstUserTextStripsEnvelope(t *testing.T) {
	s := &model.Session{Messages: []model.Message{{
		Role: model.RoleUser,
		Text: "<user_info>noise</user_info><user_query>真正的问题</user_query>trailing",
	}}}
	if got := firstUserText(s); got != "真正的问题" {
		t.Errorf("got %q", got)
	}
	plain := &model.Session{Messages: []model.Message{{Role: model.RoleUser, Text: "plain"}}}
	if got := firstUserText(plain); got != "plain" {
		t.Errorf("got %q", got)
	}
}

// A root blob that references a missing child must not fail the whole load.
func TestMissingBlobFailSoft(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ws", "sess-1")
	buildStore(t, dir)

	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	// drop one message blob, keep the root pointing at it
	id := make([]byte, 32)
	id[0] = 4 // the tool message
	if _, err := db.Exec(`DELETE FROM blobs WHERE id = ?`, hex.EncodeToString(id)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := &Source{dir: base}
	refs, _ := s.List(context.Background(), time.Time{})
	sess, err := s.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatalf("missing blob must not be fatal: %v", err)
	}
	if len(sess.Messages) != 2 {
		t.Errorf("want 2 surviving messages, got %d", len(sess.Messages))
	}
}

func TestListSkipsEmptyStore(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ws", "empty-sess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Source{dir: base}
	refs, err := s.List(context.Background(), time.Time{})
	if err != nil || len(refs) != 0 {
		t.Errorf("empty store.db must be skipped: %v %v", refs, err)
	}
}
