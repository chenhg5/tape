package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `tape search <agent>` and `tape search <project-name>` must hit the
// synthetic @meta row that the indexer plants per session.
func TestSearchHitsAgentAndProjectMeta(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// every codex session is reachable by typing the agent name
	d := e.mustRun(0, "search", "codex").data(t)
	hits := d["hits"].([]any)
	hasCodex := false
	for _, h := range hits {
		if h.(map[string]any)["agent"] == "codex" {
			hasCodex = true
			break
		}
	}
	if !hasCodex {
		t.Fatalf("search 'codex' should hit codex sessions via @meta, got %v", hits)
	}

	// project basename also resolves
	if d := e.mustRun(0, "search", "demo").data(t); d["count"].(float64) < 1 {
		t.Errorf("search 'demo' (project basename) found 0 hits, want >=1")
	}
}

// `tape backup export --since` writes a smaller artifact containing only
// recently-updated sessions.
func TestBackupExportIncremental(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	fullPath := filepath.Join(t.TempDir(), "full.tar.zst")
	e.mustRun(0, "backup", "export", "--output", fullPath)
	fullSize := fileSize(t, fullPath)

	// A horizon in the far future excludes every session — proves the
	// filter actually applies and the artifact is smaller (the empty
	// tar.zst still contains zstd framing bytes, so we only assert it
	// is strictly less than the full one).
	incrPath := filepath.Join(t.TempDir(), "incr.tar.zst")
	r := e.mustRun(0, "backup", "export", "--since", "9999-01-01", "--output", incrPath)
	res := r.data(t)["result"].(map[string]any)
	if res["action"] != "export-incremental" {
		t.Fatalf("incremental export action = %v", res["action"])
	}
	if n, _ := res["changed_files"].(float64); n != 0 {
		t.Errorf("9999-01-01 horizon should yield 0 files, got %v", res)
	}
	if fileSize(t, incrPath) >= fullSize {
		t.Errorf("incremental %d should be smaller than full %d",
			fileSize(t, incrPath), fullSize)
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return st.Size()
}

// Search filters must compose with the query end to end.
func TestSearchFilters(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "search", "构建速度", "--agent", "codex").data(t)
	if d["count"].(float64) < 1 {
		t.Errorf("agent filter: %v", d["count"])
	}
	// wrong agent: no hits, exit 3
	e.mustRun(3, "search", "构建速度", "--agent", "cursor")

	// --dir narrows by the session's working directory. The fixture's
	// codex seed lives under /root/code/demo, so the filter should hit.
	d = e.mustRun(0, "search", "迁移", "--dir", "/root/code/demo").data(t)
	if d["count"].(float64) < 1 {
		t.Errorf("dir filter: %v", d["count"])
	}
}

// `tape schema <path>` must resolve nested commands; unknown paths are
// usage errors.
func TestSchemaSubcommand(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	r := e.mustRun(0, "schema", "backup", "push")
	var envelope struct {
		Data struct {
			Name  string `json:"name"`
			Flags []struct {
				Name string `json:"name"`
			} `json:"flags"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Name != "push" {
		t.Errorf("schema name = %q", envelope.Data.Name)
	}
	flags := map[string]bool{}
	for _, f := range envelope.Data.Flags {
		flags[f.Name] = true
	}
	for _, want := range []string{"--remote", "--dry-run", "--allow-secrets"} {
		if !flags[want] {
			t.Errorf("schema missing flag %s", want)
		}
	}

	if r := e.run("schema", "nonsense"); r.code != 2 {
		t.Errorf("unknown schema path: exit %d, want 2", r.code)
	}
}

func TestRestoreLastOnEmptyArchive(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	if r := e.run("restore", "@last", "--to", "codex"); r.code != 3 {
		t.Errorf("@last on empty archive: exit %d, want 3", r.code)
	}
}

// The index is derived state: wiping it must be fully recoverable
// without the user having to know `tape index rebuild` exists. A second
// sync notices the gap and refills the index on its own.
func TestIndexIsDisposable(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")
	e.mustRun(0, "search", "stateless")

	if err := os.RemoveAll(filepath.Join(e.dir, "index")); err != nil {
		t.Fatal(err)
	}
	// gone: no hits (fresh empty index), but no crash
	e.mustRun(3, "search", "stateless")

	// no `index rebuild` involved: sync alone heals it.
	e.mustRun(0, "sync")
	e.mustRun(0, "search", "stateless")
}

// --dir on ls/search is the *project* filter, not tape's data dir.
// Passing a path that doesn't appear in any session's cwd must return
// "no results" (exit 3); passing the project that actually owns the
// sessions must return them. This pins the renamed semantics so we
// don't accidentally regress to the old "data dir" meaning.
func TestLsDirIsProjectFilter(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// Unrelated path: zero results, no scary error.
	r := e.run("ls", "--dir", t.TempDir())
	if r.code != 3 {
		t.Fatalf("ls --dir <unknown> exit=%d, want 3 (no results)", r.code)
	}

	// Every seeded session lives under /root/code/demo, so filtering on
	// that path must return all three agents' archived sessions.
	d := e.mustRun(0, "ls", "--dir", "/root/code/demo").data(t)
	if d["count"].(float64) < 3 {
		t.Fatalf("ls --dir /root/code/demo: count=%v want >=3", d["count"])
	}
}

// Cobra's default "accepts 1 arg(s), received 0" tells users nothing.
// Our custom Args validator points them at the exact command to type.
func TestFriendlyArgErrors(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	// no archive needed: validators fire before RunE.

	for _, tc := range []struct {
		name     string
		args     []string
		mustHave string
	}{
		{"restore-no-args", []string{"restore"}, "missing session id"},
		{"restore-no-to", []string{"restore", "@last"}, "--to"},
		{"show-no-args", []string{"show"}, "missing session id"},
		{"search-no-args", []string{"search"}, "missing search query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.run(tc.args...)
			if r.code != 2 {
				t.Errorf("%s exit %d, want 2", tc.name, r.code)
			}
			// json error body carries the helpful text
			if !strings.Contains(r.stderr, tc.mustHave) {
				t.Errorf("%s stderr lacks %q:\n%s", tc.name, tc.mustHave, r.stderr)
			}
			// cobra's terse default ("accepts 1 arg(s)") must NOT leak
			if strings.Contains(r.stderr, "accepts 1 arg") {
				t.Errorf("%s leaked cobra default error:\n%s", tc.name, r.stderr)
			}
		})
	}
}

// Cursor sessions carry no per-message timestamps; the index must fall
// back to session.updated_at so every hit renders a real time.
func TestCursorHitsHaveTimestamps(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "search", "迁移", "--agent", "cursor").data(t)
	hits := d["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("no cursor hits")
	}
	ts, _ := hits[0].(map[string]any)["timestamp"].(string)
	if ts == "" || strings.HasPrefix(ts, "0001-01-01") {
		t.Errorf("cursor hit has no timestamp, got %q", ts)
	}
}

// Subcommand prefix matching lets users skip a few keystrokes. Each
// unambiguous prefix should resolve to its full command; ambiguous ones
// must fail loudly so we don't pick the wrong command silently.
func TestSubcommandPrefixMatching(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync") // populate so `ls` returns rows

	// Unambiguous prefixes
	for _, args := range [][]string{
		{"sy"},  // sync
		{"sho"}, // show … needs an id arg below, but resolution is the test
		{"se"},  // search
		{"sc"},  // schema
		{"ov"},  // overview
		{"re"},  // restore
	} {
		// run with --help to avoid needing real args; we're just probing
		// whether cobra resolved the command name.
		r := e.run(append(args, "--help")...)
		if r.code != 0 {
			t.Errorf("prefix %v --help: code=%d stderr=%s", args, r.code, r.stderr)
		}
	}

	// Ambiguous prefix: "s" matches sync/search/show/schema → must fail.
	r := e.run("s", "--help")
	if r.code == 0 {
		t.Errorf("ambiguous 's' silently resolved: %s", r.stdout)
	}
}

// Piped/agent mode must keep the strict contract: missing args = usage error.
// (Interactive picker only kicks in on a real TTY.)
func TestRestoreNonInteractivePipedMissingArgs(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	r := e.run("restore") // stdout is a pipe in our harness
	if r.code != 2 {
		t.Fatalf("restore (piped) exit=%d want 2; stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "missing session id") {
		t.Errorf("expected helpful error, got: %s", r.stderr)
	}
}

// @last is a shorthand for the most recently updated session. It used
// to only work in restore; every command that takes a session id should
// honor it for consistency.
func TestAtLastShorthandWorksForShow(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "show", "@last").data(t)
	if d["id"] == nil || d["agent"] == nil {
		t.Fatalf("show @last returned no session: %v", d)
	}
}

// Piped `tape show` (no TTY) without an id is a usage error with a
// helpful suggestion list, not a silent hang.
func TestShowNonInteractivePipedMissingArg(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	r := e.run("show")
	if r.code != 2 {
		t.Fatalf("show (piped) exit=%d want 2; stderr=%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "missing session id") {
		t.Errorf("missing suggestion: %s", r.stderr)
	}
}

// Transcript strategy: full verbatim conversation in markdown, no LLM
// call. The output must contain both user and assistant lines for the
// session — round-tripping fidelity is the whole point of this mode.
func TestRestoreTranscriptStrategy(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	out := filepath.Join(t.TempDir(), "transcript.md")
	d := e.mustRun(0, "restore", "@last", "--to", "claude-code",
		"--strategy", "transcript", "--output", out).data(t)
	if d["strategy"] != "transcript" {
		t.Fatalf("strategy: %v", d["strategy"])
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"### 👤 User", "### 🤖 Assistant", "Session transcript"} {
		if !strings.Contains(s, want) {
			t.Errorf("transcript missing %q:\n%s", want, s)
		}
	}
}

// Memory strategy: writes the transcript AND appends an @-reference into
// the project memory file (CLAUDE.md or AGENTS.md), keyed by target.
// Re-running must be idempotent — no stacked tape blocks.
func TestRestoreMemoryStrategyInjectsAndIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// pick the codex session so target = AGENTS.md (cross-agent convention)
	d := e.mustRun(0, "ls", "--agent", "codex").data(t)
	sessions := d["sessions"].([]any)
	if len(sessions) == 0 {
		t.Fatal("no codex sessions seeded")
	}
	id := sessions[0].(map[string]any)["id"].(string)

	// The session's cwd is /root/code/demo which doesn't exist in the
	// test env. Memory strategy writes files relative to session.CWD by
	// default — we use --output to redirect to a writable temp dir.
	root := t.TempDir()
	out := filepath.Join(root, ".tape-handoff.md")

	for i := 0; i < 3; i++ {
		d := e.mustRun(0, "restore", id, "--to", "cursor",
			"--strategy", "memory", "--output", out).data(t)
		if d["strategy"] != "memory" {
			t.Fatalf("strategy: %v", d["strategy"])
		}
	}

	// transcript file exists with real content
	body, _ := os.ReadFile(out)
	if !strings.Contains(string(body), "👤 User") {
		t.Errorf("memory transcript empty: %s", body)
	}
	// AGENTS.md was created and contains exactly one tape block
	mem, err := os.ReadFile(filepath.Join(filepath.Dir(out), "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md not written: %v", err)
	}
	if c := strings.Count(string(mem), "tape:handoff:start"); c != 1 {
		t.Errorf("expected exactly one tape block, got %d:\n%s", c, mem)
	}
	if !strings.Contains(string(mem), ".tape-handoff.md") {
		t.Errorf("memory file should reference handoff: %s", mem)
	}
}

// claude-code's memory file is CLAUDE.md, not AGENTS.md — covers the
// per-agent path table in restore/memory.go.
func TestRestoreMemoryStrategyClaudeCodeWritesClaudeMd(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")
	// Restore the cursor session to claude-code so we target CLAUDE.md.
	d := e.mustRun(0, "ls", "--agent", "cursor").data(t)
	sessions := d["sessions"].([]any)
	id := sessions[0].(map[string]any)["id"].(string)
	root := t.TempDir()
	out := filepath.Join(root, ".tape-handoff.md")
	e.mustRun(0, "restore", id, "--to", "claude-code",
		"--strategy", "memory", "--output", out)
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "CLAUDE.md")); err != nil {
		t.Fatalf("CLAUDE.md not written for claude-code target: %v", err)
	}
}

// `tape index rebuild` still works as an escape hatch even though it is
// hidden from `tape --help`.
func TestHiddenIndexRebuildStillRuns(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "index", "rebuild").data(t)
	if d["indexed"].(float64) < 3 {
		t.Fatalf("rebuild: %v", d)
	}
}

// TAPE_HOME relocates tape's own archive. We removed the global --dir
// flag (it confused users into thinking it meant "project filter"), so
// the env var is now the single way to multi-tenant tape on one box.
func TestTapeHomeEnvOverride(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	other := t.TempDir()

	// Sync into the alternate home using the env var.
	if r := e.runEnv(map[string]string{"TAPE_HOME": other}, "sync"); r.code != 0 {
		t.Fatalf("sync TAPE_HOME=%s: code=%d stderr=%s", other, r.code, r.stderr)
	}
	// The original home stayed empty (sync went elsewhere).
	e.mustRun(3, "ls")
	// The override location holds the data.
	d := e.runEnv(map[string]string{"TAPE_HOME": other}, "ls").data(t)
	if d["count"].(float64) != 3 {
		t.Errorf("ls under TAPE_HOME=%s: count=%v want 3", other, d["count"])
	}
	if entries, _ := os.ReadDir(filepath.Join(other, "archive")); len(entries) == 0 {
		t.Error("override TAPE_HOME has no archive/")
	}
}

// Legacy TAPE_DIR env keeps working so old shell rcfiles aren't broken.
func TestLegacyTapeDirEnvStillWorks(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	other := t.TempDir()

	e.runEnv(map[string]string{"TAPE_DIR": other, "TAPE_HOME": ""}, "sync")
	d := e.runEnv(map[string]string{"TAPE_DIR": other, "TAPE_HOME": ""}, "ls").data(t)
	if d["count"].(float64) != 3 {
		t.Errorf("legacy TAPE_DIR: count=%v want 3", d["count"])
	}
}

// Pagination contract: ls and search expose page/page_size/has_more so an
// agent can iterate. Out-of-range pages return exit 3.
func TestPagination(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// page 1 of 2: two items, more follow
	d := e.mustRun(0, "ls", "--limit", "2", "--page", "1").data(t)
	if d["count"] != float64(2) {
		t.Fatalf("page1 count: %v", d["count"])
	}
	if d["total"] != float64(3) {
		t.Fatalf("page1 total: %v", d["total"])
	}
	if d["has_more"] != true {
		t.Fatalf("page1 has_more: %v", d["has_more"])
	}

	// page 2: one item, no more
	d = e.mustRun(0, "ls", "--limit", "2", "--page", "2").data(t)
	if d["count"] != float64(1) || d["has_more"] != false {
		t.Fatalf("page2: %v", d)
	}

	// page 3: empty, exit 3
	if r := e.run("ls", "--limit", "2", "--page", "3"); r.code != 3 {
		t.Fatalf("page3: exit %d, want 3", r.code)
	}

	// search exposes the same shape; single-hit fixtures still set has_more
	// to false correctly.
	d = e.mustRun(0, "search", "stateless", "--limit", "1", "--page", "1").data(t)
	if d["count"] != float64(1) || d["has_more"] != false || d["page"] != float64(1) {
		t.Fatalf("search page1: %v", d)
	}

	// invalid page numbers are usage errors
	if r := e.run("ls", "--page", "0"); r.code != 2 {
		t.Errorf("page=0: exit %d, want 2", r.code)
	}
	if r := e.run("search", "stateless", "--limit", "0"); r.code != 2 {
		t.Errorf("limit=0: exit %d, want 2", r.code)
	}
}

// Stdout JSON contract: every successful data-producing command wraps its
// payload in the same envelope.
func TestJSONEnvelopeEverywhere(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync") // consume the first sync; now assert on each command

	for _, args := range [][]string{
		{"sync"},
		{"ls"},
		{"search", "stateless"},
		{"show", "7dd2afaf"},
		{"index", "rebuild"},
		{"backup", "scan"},
	} {
		r := e.mustRun(0, args...)
		var envelope struct {
			SchemaVersion *int `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil || envelope.SchemaVersion == nil {
			t.Errorf("tape %s: output not enveloped: %v\n%s", strings.Join(args, " "), err, r.stdout)
		}
	}
}
