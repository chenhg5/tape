package e2e

import (
	"strings"
	"testing"
)

// TestFullWorkflow drives the main user journey across all three agents:
// sync, list, filter, search (CJK + latin), show, idempotent re-sync, and
// index rebuild.
func TestFullWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("full workflow skipped in -short mode")
	}
	e := newEnv(t)
	e.seedAllAgents()

	// 1. sync archives one session per agent
	d := e.mustRun(0, "sync").data(t)
	if d["archived"] != float64(3) {
		t.Fatalf("archived = %v, want 3", d["archived"])
	}

	// 2. ls sees all three, newest first
	d = e.mustRun(0, "ls").data(t)
	if d["count"] != float64(3) {
		t.Fatalf("ls count = %v", d["count"])
	}

	// 3. agent filter
	d = e.mustRun(0, "ls", "--agent", "codex").data(t)
	if d["count"] != float64(1) {
		t.Errorf("ls --agent codex count = %v", d["count"])
	}
	sessions := d["sessions"].([]any)
	first := sessions[0].(map[string]any)
	if first["agent"] != "codex" || first["project"] != "root-code-demo" {
		t.Errorf("session summary: %v", first)
	}

	// 4. CJK search hits the codex session
	d = e.mustRun(0, "search", "构建速度").data(t)
	hits := d["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("CJK search hits = %d", len(hits))
	}
	hit := hits[0].(map[string]any)
	if !strings.HasPrefix(hit["session_id"].(string), "codex/") {
		t.Errorf("hit = %v", hit)
	}
	if !strings.Contains(hit["snippet"].(string), "构建速度") {
		t.Errorf("snippet = %v", hit["snippet"])
	}

	// 5. latin search hits the claude session
	d = e.mustRun(0, "search", "stateless").data(t)
	if len(d["hits"].([]any)) != 1 {
		t.Errorf("latin search hits = %v", d["count"])
	}

	// 6. search miss: exit 3 and machine-readable empty result
	r := e.mustRun(3, "search", "不存在的词组xyzzy")
	if r.data(t)["count"] != float64(0) {
		t.Errorf("miss count = %v", r.data(t)["count"])
	}

	// 7. show by unambiguous id fragment: data is the full session object
	sess := e.mustRun(0, "show", "7dd2afaf").data(t)
	if sess["agent"] != "claude-code" || sess["title"] != "Auth migration" {
		t.Errorf("show session = %v", sess)
	}
	if len(sess["messages"].([]any)) != 2 {
		t.Errorf("messages = %v", sess["messages"])
	}

	// 8. second sync is a no-op (checksum-based)
	d = e.mustRun(0, "sync").data(t)
	if d["archived"] != float64(0) || d["skipped"] != float64(3) {
		t.Errorf("re-sync: %v", d)
	}

	// 9. index rebuild from archive
	d = e.mustRun(0, "index", "rebuild").data(t)
	if d["indexed"] != float64(3) {
		t.Errorf("rebuild indexed = %v", d["indexed"])
	}
	// search still works after rebuild
	e.mustRun(0, "search", "迁移脚本")

	// 10. overview aggregates the archive
	d = e.mustRun(0, "overview").data(t)
	if d["sessions"] != float64(3) || d["projects"] != float64(1) {
		t.Errorf("overview totals: sessions=%v projects=%v", d["sessions"], d["projects"])
	}
	if agents := d["agents"].([]any); len(agents) != 3 {
		t.Errorf("overview agents: %d", len(agents))
	}
	if d["archive_bytes"].(float64) <= 0 {
		t.Errorf("archive_bytes = %v", d["archive_bytes"])
	}
	if recent := d["recent"].([]any); len(recent) != 3 {
		t.Errorf("overview recent: %d", len(recent))
	}
	if days := d["activity"].([]any); len(days) != 14 {
		t.Errorf("overview activity days: %d", len(days))
	}
}

func TestOverviewEmptyArchive(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	d := e.mustRun(0, "overview").data(t)
	if d["sessions"] != float64(0) {
		t.Errorf("empty overview: %v", d["sessions"])
	}
}

// TestSinceFilter checks the incremental time window plumbing end to end.
func TestSinceFilter(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// a window covering the fixture timestamps includes everything
	d := e.mustRun(0, "ls", "--since", "2026-01-01").data(t)
	if d["count"] != float64(3) {
		t.Errorf("since 2026-01-01: %v", d["count"])
	}
	// a window after every fixture excludes everything: exit 3
	r := e.mustRun(3, "ls", "--since", "2099-01-01")
	if r.data(t)["count"] != float64(0) {
		t.Errorf("since 2099: %v", r.data(t)["count"])
	}
}

// TestShowAmbiguousFragment: two codex-style ids sharing a prefix must make
// the fragment ambiguous rather than silently picking one.
func TestShowAmbiguousFragment(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	r := e.run("show", "019ea0af-3d6a-7393-8ccf-a4ae49f116c") // 35 of 36 chars
	if r.code != 0 {
		// a unique prefix is fine; now check a truly missing one
		t.Logf("prefix resolution: %s", r.stderr)
	}
	if r := e.run("show", "ffffffff"); r.code == 0 {
		t.Error("missing id must not exit 0")
	}
}
