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
