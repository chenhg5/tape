// Package e2e tests the compiled tape binary end to end: a fake HOME with
// realistic fixtures for all three agents, a private TAPE_DIR, and
// assertions on stdout JSON, stderr and exit codes.
//
// Run with: go test ./test/e2e/  (the binary is built once in TestMain)
// Smoke subset: go test -short ./test/e2e/
package e2e

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

var tapeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tape-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	tapeBin = filepath.Join(dir, "tape")
	args := []string{"build", "-o", tapeBin}
	// E2E_COVER=1 instruments the binary; counters land in GOCOVERDIR
	// (propagated to the subprocess via os.Environ in env.run)
	if os.Getenv("E2E_COVER") != "" {
		args = append(args, "-cover", "-coverpkg=github.com/chenhg5/tape/...")
	}
	build := exec.Command("go", append(args, "../../cmd/tape")...)
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// env is one isolated tape installation: its own HOME and TAPE_HOME.
type env struct {
	t        *testing.T
	home     string
	dir      string // TAPE_HOME (tape's archive/index live here)
	pathPrep string // prepended to PATH (for stub binaries like ssh)
}

type result struct {
	stdout, stderr string
	code           int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return &env{t: t, home: t.TempDir(), dir: t.TempDir()}
}

func (e *env) run(args ...string) result {
	return e.runEnv(nil, args...)
}

// runEnv lets a test override (or unset, by passing "") specific env vars
// for one invocation; useful for exercising TAPE_HOME / TAPE_DIR fallbacks
// without spinning up a whole new env fixture.
func (e *env) runEnv(extra map[string]string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(tapeBin, args...)
	environ := os.Environ()
	if e.pathPrep != "" {
		for i, kv := range environ {
			if strings.HasPrefix(kv, "PATH=") {
				environ[i] = "PATH=" + e.pathPrep + ":" + strings.TrimPrefix(kv, "PATH=")
			}
		}
	}
	base := append(environ,
		"HOME="+e.home,
		"TAPE_HOME="+e.dir,
		"NO_COLOR=1",
	)
	for k, v := range extra {
		// Drop any existing entry for k so the override wins regardless of
		// how Go's exec resolves duplicates on the current platform.
		filtered := base[:0]
		prefix := k + "="
		for _, kv := range base {
			if !strings.HasPrefix(kv, prefix) {
				filtered = append(filtered, kv)
			}
		}
		base = append(filtered, k+"="+v)
	}
	cmd.Env = base
	// Run inside the fake HOME so any "default cwd" behaviour from tape
	// (e.g. `restore --strategy memory`'s implicit `.tape-handoff.md`)
	// scribbles into the tempdir, not the repo's test/e2e directory.
	cmd.Dir = e.home
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("running tape %v: %v", args, err)
	}
	return result{stdout: out.String(), stderr: errBuf.String(), code: code}
}

// mustRun fails the test unless the command exits with wantCode.
func (e *env) mustRun(wantCode int, args ...string) result {
	e.t.Helper()
	r := e.run(args...)
	if r.code != wantCode {
		e.t.Fatalf("tape %s: exit %d, want %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), r.code, wantCode, r.stdout, r.stderr)
	}
	return r
}

// data decodes the {"schema_version":1,"data":...} JSON contract.
func (r result) data(t *testing.T) map[string]any {
	t.Helper()
	var envelope struct {
		SchemaVersion int            `json:"schema_version"`
		Data          map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil {
		t.Fatalf("stdout is not the JSON envelope: %v\n%s", err, r.stdout)
	}
	if envelope.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d", envelope.SchemaVersion)
	}
	return envelope.Data
}

// errJSON decodes the structured error written to stderr in robot mode.
func (r result) errJSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	// stderr can hold multiple lines (findings etc.); the error object is last
	lines := strings.Split(strings.TrimSpace(r.stderr), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, r.stderr)
	}
	return m
}

// --- fixtures -------------------------------------------------------------

const (
	claudeSessionID = "7dd2afaf-aaaa-bbbb-cccc-000000000001"
	codexSessionID  = "019ea0af-3d6a-7393-8ccf-a4ae49f116c3"
	cursorSessionID = "f1ff7b74-0a28-4dd4-a452-000000000003"
)

func (e *env) seedAllAgents() {
	e.t.Helper()
	e.seedClaude()
	e.seedCodex()
	e.seedCursor()
}

// Phase-A fixtures: Gemini-family agents share a JSONL transport, Aider
// uses one merged markdown file. SessionIDs are fixed so resolveSessionID
// tests have a stable target.
const (
	geminiSessionID = "d7501ec6"
	qwenSessionID   = "session-001"
	iflowSessionID  = "f1f1f1f1"
)

func (e *env) seedGemini() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".gemini", "tmp", "abc123", "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"sessionId":"` + geminiSessionID + `","projectHash":"abc123","startTime":"2026-06-12T10:00:00.000Z","directories":["/root/code/demo"]}`,
		`{"id":"m1","type":"user","content":"先看下 webpack 配置","timestamp":"2026-06-12T10:00:01.000Z"}`,
		`{"id":"m2","type":"gemini","content":"webpack.config.js 用了 babel-loader","timestamp":"2026-06-12T10:00:02.000Z","model":"gemini-2.5-pro"}`,
	}
	path := filepath.Join(dir, "session-2026-06-12T10-00-00-"+geminiSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedQwen() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".qwen", "projects", "abc123", "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"uuid":"u1","parentUuid":null,"sessionId":"` + qwenSessionID + `","timestamp":"2026-06-12T10:00:00.000Z","type":"user","cwd":"/root/code/demo","gitBranch":"main","message":{"role":"user","parts":[{"text":"实现 jwt 鉴权"}]}}`,
		`{"uuid":"a1","parentUuid":"u1","sessionId":"` + qwenSessionID + `","timestamp":"2026-06-12T10:00:01.000Z","type":"assistant","model":"qwen2.5-coder-30b","message":{"role":"model","parts":[{"text":"先看现有 auth.go"}]}}`,
	}
	path := filepath.Join(dir, qwenSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedIFlow() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".iflow", "projects", "abc123")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"sessionId":"` + iflowSessionID + `","projectHash":"abc123","startTime":"2026-06-12T10:00:00.000Z","directories":["/root/code/demo"]}`,
		`{"id":"m1","type":"user","content":"翻译这段中文文档","timestamp":"2026-06-12T10:00:01.000Z"}`,
		`{"id":"m2","type":"gemini","content":"Translation: ...","timestamp":"2026-06-12T10:00:02.000Z","model":"deepseek-v3"}`,
	}
	path := filepath.Join(dir, "session-2026-06-12T10-00-"+iflowSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// opencodeSessionID matches the row id we seed into opencode.db below.
const opencodeSessionID = "ses_opencode_demo"

const (
	antigravitySessionID = "11111111-2222-3333-4444-555555555555"
	qoderSessionID       = "qoder-s-001"
	mimocodeSessionID    = "ses_mimo_demo"
	kimiCodeSessionID    = "kc-2026-06-13-001"
)

func (e *env) seedAntigravity() {
	e.t.Helper()
	logs := filepath.Join(e.home, ".gemini", "antigravity-cli", "brain", antigravitySessionID, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-24T12:14:37Z","content":"What is Antigravity CLI?"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-24T12:14:38Z","content":"Antigravity is Google's successor to gemini-cli, with a new transcript format."}`,
	}
	path := filepath.Join(logs, "transcript_full.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedQoder() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".qoder", "projects", "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"uuid":"u1","parentUuid":null,"sessionId":"` + qoderSessionID + `","timestamp":"2026-06-12T10:00:00.000Z","type":"user","cwd":"/root/code/demo","message":{"role":"user","parts":[{"text":"qoder 怎么用"}]}}`,
		`{"uuid":"a1","parentUuid":"u1","sessionId":"` + qoderSessionID + `","timestamp":"2026-06-12T10:00:01.000Z","type":"assistant","model":"qoder-pro","message":{"role":"model","parts":[{"text":"参考 docs.qoder.com"}]}}`,
	}
	path := filepath.Join(dir, qoderSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	// Sidecar that List() must filter out.
	_ = os.WriteFile(filepath.Join(dir, qoderSessionID+"-session.json"), []byte(`{}`), 0o600)
}

func (e *env) seedOpenCode() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()

	for _, q := range []string{
		`CREATE TABLE project (id TEXT PRIMARY KEY, directory TEXT NOT NULL)`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER, time_updated INTEGER, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL,
			session_id TEXT NOT NULL, time_created INTEGER, time_updated INTEGER,
			data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			e.t.Fatal(err)
		}
	}
	created := int64(1781256000000)
	_, _ = db.Exec(`INSERT INTO project VALUES (?,?)`, "prj_demo", "/root/code/demo")
	_, _ = db.Exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?,?,?)`,
		opencodeSessionID, "prj_demo", nil, "demo", "/root/code/demo",
		"Improve test coverage", "1.5.0", created, created+10_000)

	uData := `{"id":"m1","sessionID":"` + opencodeSessionID + `","role":"user","time":{"created":` + itoa(created+100) + `}}`
	aData := `{"id":"m2","sessionID":"` + opencodeSessionID + `","role":"assistant","time":{"created":` + itoa(created+5000) + `},"model":{"providerID":"anthropic","modelID":"claude-fable-5"},"path":{"cwd":"/root/code/demo","root":"/root/code/demo"}}`
	_, _ = db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`, "m1", opencodeSessionID, created+100, created+100, uData)
	_, _ = db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`, "m2", opencodeSessionID, created+5000, created+5000, aData)

	parts := []struct {
		id, mid string
		t       int64
		data    string
	}{
		{"p1", "m1", created + 200, `{"type":"text","text":"补 e2e 测试覆盖"}`},
		{"p2", "m2", created + 3000, `{"type":"reasoning","text":"先看现有 test/e2e"}`},
		{"p3", "m2", created + 4000, `{"type":"tool","tool":"list_dir","callID":"c1","state":{"status":"completed","input":{"path":"test/e2e"},"output":"e2e_test.go\nworkflow_test.go"}}`},
		{"p4", "m2", created + 5500, `{"type":"text","text":"已加 phase_a_test 覆盖四家 agent"}`},
	}
	for _, p := range parts {
		if _, err := db.Exec(`INSERT INTO part VALUES (?,?,?,?,?,?)`,
			p.id, p.mid, opencodeSessionID, p.t, p.t, p.data); err != nil {
			e.t.Fatal(err)
		}
	}
}

// seedMimocode plants a MiMo Code session at the canonical
// ~/.local/share/mimocode/mimocode.db path. MiMo Code is a fork of
// sst/opencode (same Drizzle session/message/part schema), so the
// fixture mirrors seedOpenCode — only the path and agent label change.
func (e *env) seedMimocode() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".local", "share", "mimocode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mimocode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()

	for _, q := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER, time_updated INTEGER, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL,
			session_id TEXT NOT NULL, time_created INTEGER, time_updated INTEGER,
			data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			e.t.Fatal(err)
		}
	}
	created := int64(1781256000000)
	_, _ = db.Exec(`INSERT INTO session VALUES (?,?,?,?,?,?,?,?,?)`,
		mimocodeSessionID, nil, nil, "mimo-demo", "/root/code/demo",
		"Try MiMo Code", "0.1.0", created, created+10_000)

	uData := `{"id":"m1","sessionID":"` + mimocodeSessionID + `","role":"user","time":{"created":` + itoa(created+100) + `}}`
	aData := `{"id":"m2","sessionID":"` + mimocodeSessionID + `","role":"assistant","time":{"created":` + itoa(created+5000) + `},"model":{"providerID":"xiaomi","modelID":"mimo-pro"},"path":{"cwd":"/root/code/demo","root":"/root/code/demo"}}`
	_, _ = db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`, "m1", mimocodeSessionID, created+100, created+100, uData)
	_, _ = db.Exec(`INSERT INTO message VALUES (?,?,?,?,?)`, "m2", mimocodeSessionID, created+5000, created+5000, aData)

	parts := []struct {
		id, mid string
		t       int64
		data    string
	}{
		{"p1", "m1", created + 200, `{"type":"text","text":"小米 MiMo Code 怎么用？"}`},
		{"p2", "m2", created + 4000, `{"type":"text","text":"MEMORY.md 会自动注入；--continue 续接最近会话。"}`},
	}
	for _, p := range parts {
		if _, err := db.Exec(`INSERT INTO part VALUES (?,?,?,?,?,?)`,
			p.id, p.mid, mimocodeSessionID, p.t, p.t, p.data); err != nil {
			e.t.Fatal(err)
		}
	}
}

// seedKimiCode plants a kimi-code session under
// ~/.kimi-code/sessions/<workDirKey>/<sessionId>/agents/main/wire.jsonl —
// the JSON-RPC 2.0 event log kimi-code persists per session. The
// fixture exercises both a string user_input and the ToolCall →
// ToolResult flow so the e2e search/show pass actually hits the
// parser's main branches.
func (e *env) seedKimiCode() {
	e.t.Helper()
	mainDir := filepath.Join(e.home, ".kimi-code", "sessions", "wd_demo", kimiCodeSessionID, "agents", "main")
	if err := os.MkdirAll(mainDir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	state := `{"title":"试试 kimi code","workDir":"/root/code/demo","model":"kimi-k2-preview","createdAt":1781256000000}`
	if err := os.WriteFile(filepath.Join(e.home, ".kimi-code", "sessions", "wd_demo", kimiCodeSessionID, "state.json"),
		[]byte(state), 0o600); err != nil {
		e.t.Fatal(err)
	}

	lines := []string{
		`{"jsonrpc":"2.0","method":"event","params":{"type":"TurnBegin","payload":{"user_input":"用 kimi code 写一个登录页"}}}`,
		`{"jsonrpc":"2.0","method":"event","params":{"type":"ContentPart","payload":{"type":"text","text":"好的，我来生成。"}}}`,
		`{"jsonrpc":"2.0","method":"event","params":{"type":"ToolCall","payload":{"type":"function","id":"tc-1","function":{"name":"Write","arguments":"{\"path\":\"login.tsx\"}"}}}}`,
		`{"jsonrpc":"2.0","method":"event","params":{"type":"ToolResult","payload":{"tool_call_id":"tc-1","return_value":{"is_error":false,"output":"wrote login.tsx"}}}}`,
		`{"jsonrpc":"2.0","method":"event","params":{"type":"TurnEnd","payload":{}}}`,
	}
	if err := os.WriteFile(filepath.Join(mainDir, "wire.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		e.t.Fatal(err)
	}

	// Also write the session_index.jsonl entry — proves the index
	// fast path is exercised, not just the directory walk.
	if err := os.WriteFile(filepath.Join(e.home, ".kimi-code", "session_index.jsonl"),
		[]byte(`{"sessionId":"`+kimiCodeSessionID+`","sessionDir":"sessions/wd_demo/`+kimiCodeSessionID+`","workDir":"/root/code/demo"}`+"\n"),
		0o600); err != nil {
		e.t.Fatal(err)
	}
}

// itoa avoids strconv import in this giant fixture helper file.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (e *env) seedAider() {
	e.t.Helper()
	body := `# aider chat started at 2026-06-12 10:30:45

#### refactor the http handler

Looking at handler.go — I'll extract the validation block.

#### ship it

Done. Tests pass.
`
	path := filepath.Join(e.home, ".aider.chat.history.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedClaude() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".claude", "projects", "-root-code-demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"type":"ai-title","aiTitle":"Auth migration"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-06-01T10:00:00Z","cwd":"/root/code/demo","gitBranch":"main","version":"2.0.0","message":{"role":"user","content":"为什么不用 OAuth2 而是 JWT?"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-06-01T10:00:30Z","message":{"role":"assistant","content":[{"type":"text","text":"因为无状态的 stateless token 扩展性更好。"}]}}`,
	}
	path := filepath.Join(dir, claudeSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedCodex() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".codex", "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	lines := []string{
		`{"timestamp":"2026-06-07T14:04:59.000Z","type":"session_meta","payload":{"id":"` + codexSessionID + `","timestamp":"2026-06-07T14:04:59.000Z","cwd":"/root/code/demo","originator":"codex_exec","cli_version":"0.137.0","source":"exec","thread_source":"user","model_provider":"openai","git":{"branch":"main"}}}`,
		`{"timestamp":"2026-06-07T14:05:00.000Z","type":"turn_context","payload":{"model":"gpt-5.3-codex","cwd":"/root/code/demo"}}`,
		`{"timestamp":"2026-06-07T14:05:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"帮我优化构建速度"}]}}`,
		`{"timestamp":"2026-06-07T14:05:20.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已开启增量编译,build is faster now."}]}}`,
	}
	path := filepath.Join(dir, "rollout-2026-06-07T14-04-59-"+codexSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedCursor() {
	e.t.Helper()
	dir := filepath.Join(e.home, ".cursor", "chats", "36e6f82a4f9ae16e16ce627c19e4b65f", cursorSessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			e.t.Fatal(err)
		}
	}
	msgs := [][]byte{
		[]byte(`{"role":"user","content":[{"type":"text","text":"<user_info>Workspace Path: /root/code/demo\n</user_info><user_query>数据库迁移脚本怎么写</user_query>"}]}`),
		[]byte(`{"role":"assistant","content":[{"type":"text","text":"用 migration files 管理 schema 变更。"}]}`),
	}
	var root []byte
	for i, m := range msgs {
		id := make([]byte, 32)
		id[0] = byte(i + 1)
		if _, err := db.Exec(`INSERT INTO blobs (id, data) VALUES (?,?)`, hex.EncodeToString(id), m); err != nil {
			e.t.Fatal(err)
		}
		root = append(root, 0x0a, 32)
		root = append(root, id...)
	}
	rootID := strings.Repeat("ff", 32)
	if _, err := db.Exec(`INSERT INTO blobs (id, data) VALUES (?,?)`, rootID, root); err != nil {
		e.t.Fatal(err)
	}
	meta := `{"agentId":"` + cursorSessionID + `","name":"DB migration","mode":"default","createdAt":1781256145540,"latestRootBlobId":"` + rootID + `","lastUsedModel":"claude-fable-5"}`
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('0', ?)`, hex.EncodeToString([]byte(meta))); err != nil {
		e.t.Fatal(err)
	}
}

// plantSecret drops a fake archived session containing an AWS key directly
// into the archive, to exercise the secret gate.
func (e *env) plantSecret() {
	e.t.Helper()
	dir := filepath.Join(e.dir, "archive", "codex", "planted", "deadbeef")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	body := `{"id":"codex/deadbeef","agent":"codex","messages":[{"role":"user","text":"key is AKIAIOSFODNN7EXAMPLE"}]}`
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
}
