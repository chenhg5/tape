package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

func writeSourceFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func demoSession(agent, id, cwd string, n int) *model.Session {
	msgs := make([]model.Message, n)
	for i := range msgs {
		msgs[i] = model.Message{Role: model.RoleUser, Text: "msg"}
	}
	return &model.Session{
		ID: agent + "/" + id, Agent: agent, SourceID: id, Title: "T " + id,
		CWD: cwd, StartedAt: time.Now().Add(-time.Hour).UTC(), UpdatedAt: time.Now().UTC(),
		Messages: msgs,
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	f := writeSourceFile(t, src, "s.jsonl", `{"hello":"world"}`)
	ref := ports.SessionRef{Agent: "codex", SourceID: "abc", Files: []string{f}}

	stale, sum, err := a.Stale(ref)
	if err != nil || !stale || sum == "" {
		t.Fatalf("new ref must be stale: stale=%v sum=%q err=%v", stale, sum, err)
	}
	sess := demoSession("codex", "abc", "/root/code/demo", 2)
	if err := a.Put(ctx, sess, ref, sum); err != nil {
		t.Fatal(err)
	}

	got, err := a.Get(ctx, "codex/abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != sess.Title || len(got.Messages) != 2 || got.CWD != sess.CWD {
		t.Errorf("round trip mismatch: %+v", got)
	}

	// raw copy must be byte-identical
	rawPath := filepath.Join(a.root, "codex", "root-code-demo", "abc", "raw", "s.jsonl")
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("raw copy missing: %v", err)
	}
	if string(data) != `{"hello":"world"}` {
		t.Errorf("raw copy altered: %q", data)
	}
}

func TestStaleLifecycle(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	f := writeSourceFile(t, src, "s.jsonl", "v1")
	ref := ports.SessionRef{Agent: "codex", SourceID: "x1", Files: []string{f}}

	_, sum, _ := a.Stale(ref)
	if err := a.Put(ctx, demoSession("codex", "x1", "/p", 1), ref, sum); err != nil {
		t.Fatal(err)
	}
	if stale, _, _ := a.Stale(ref); stale {
		t.Error("unchanged file reported stale")
	}
	writeSourceFile(t, src, "s.jsonl", "v2 changed")
	if stale, _, _ := a.Stale(ref); !stale {
		t.Error("changed file not reported stale")
	}
}

func TestListFilters(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	put := func(agent, id, cwd string) {
		f := writeSourceFile(t, src, id+".jsonl", id)
		ref := ports.SessionRef{Agent: agent, SourceID: id, Files: []string{f}}
		_, sum, _ := a.Stale(ref)
		s := demoSession(agent, id, cwd, 1)
		s.UpdatedAt = time.Now().Add(time.Duration(len(id)) * time.Minute).UTC()
		if err := a.Put(ctx, s, ref, sum); err != nil {
			t.Fatal(err)
		}
	}
	put("codex", "a1", "/root/code/alpha")
	put("codex", "b22", "/root/code/beta")
	put("claude-code", "c333", "/root/code/alpha")

	all, err := a.List(ctx, ports.Filter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d, err=%v", len(all), err)
	}
	// newest first
	if !all[0].UpdatedAt.After(all[2].UpdatedAt) {
		t.Error("not sorted by updated_at desc")
	}
	byAgent, _ := a.List(ctx, ports.Filter{Agent: "codex"})
	if len(byAgent) != 2 {
		t.Errorf("agent filter: %d", len(byAgent))
	}
	byProject, _ := a.List(ctx, ports.Filter{Project: "/root/code/alpha"})
	if len(byProject) != 2 {
		t.Errorf("project filter: %d", len(byProject))
	}
	limited, _ := a.List(ctx, ports.Filter{Limit: 1})
	if len(limited) != 1 {
		t.Errorf("limit: %d", len(limited))
	}
}

// TestListHostFilter pins the new --host scoping rule: empty matches
// everything (the historical behavior), "local" matches only sessions
// without a host stamp, any other value is an exact match. Regression
// guard for the local-vs-remote split.
func TestListHostFilter(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()

	put := func(agent, id, host string) {
		f := writeSourceFile(t, src, id+".jsonl", id)
		ref := ports.SessionRef{Agent: agent, SourceID: id, Files: []string{f}}
		_, sum, _ := a.Stale(ref)
		s := demoSession(agent, id, "/p", 1)
		if host != "" {
			s.Meta = map[string]string{"host": host}
		}
		if err := a.Put(ctx, s, ref, sum); err != nil {
			t.Fatal(err)
		}
	}
	put("codex", "loc1", "")
	put("codex", "loc2", "")
	put("codex", "rem1", "dev@build-01")
	put("codex", "rem2", "ci@runner-02")

	all, _ := a.List(ctx, ports.Filter{})
	if len(all) != 4 {
		t.Fatalf("baseline: %d", len(all))
	}

	local, _ := a.List(ctx, ports.Filter{Host: "local"})
	if len(local) != 2 {
		t.Errorf(`Host:"local" should match the 2 host-less sessions, got %d`, len(local))
	}

	build, _ := a.List(ctx, ports.Filter{Host: "dev@build-01"})
	if len(build) != 1 || build[0].ID != "codex/rem1" {
		t.Errorf(`Host:"dev@build-01" want 1×codex/rem1, got %+v`, build)
	}

	// Summary must carry the host through so picker/ls can label it.
	any, _ := a.List(ctx, ports.Filter{Host: "ci@runner-02"})
	if len(any) != 1 || any[0].Host != "ci@runner-02" {
		t.Errorf("Summary.Host not propagated: %+v", any)
	}
}

func TestListEmptyArchive(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "does-not-exist-yet"))
	out, err := a.List(context.Background(), ports.Filter{})
	if err != nil || len(out) != 0 {
		t.Errorf("empty archive: out=%v err=%v", out, err)
	}
}

func TestResolve(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	for _, id := range []string{"abc12345-1111", "abc99999-2222", "zzz00000-3333"} {
		f := writeSourceFile(t, src, id+".jsonl", id)
		ref := ports.SessionRef{Agent: "codex", SourceID: id, Files: []string{f}}
		_, sum, _ := a.Stale(ref)
		if err := a.Put(ctx, demoSession("codex", id, "/p", 1), ref, sum); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := a.Resolve(ctx, "codex/zzz00000-3333"); err != nil || got != "codex/zzz00000-3333" {
		t.Errorf("exact: %q %v", got, err)
	}
	if got, err := a.Resolve(ctx, "zzz"); err != nil || got != "codex/zzz00000-3333" {
		t.Errorf("fragment: %q %v", got, err)
	}
	if _, err := a.Resolve(ctx, "abc"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous fragment must fail, got %v", err)
	}
	if _, err := a.Resolve(ctx, "nope"); err == nil {
		t.Error("missing fragment must fail")
	}
}

func TestGetInvalidID(t *testing.T) {
	a := New(t.TempDir())
	if _, err := a.Get(context.Background(), "no-slash"); err == nil {
		t.Error("invalid id must fail")
	}
	if _, err := a.Get(context.Background(), "codex/missing"); err == nil {
		t.Error("missing session must fail")
	}
}

// A session whose normalized CWD changes between syncs must not leave a
// stale copy under the old project slug.
func TestProjectSlugMigrationDropsDuplicate(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	f := writeSourceFile(t, src, "s.jsonl", "v1")
	ref := ports.SessionRef{Agent: "cursor", SourceID: "m1", Files: []string{f}}

	_, sum, _ := a.Stale(ref)
	first := demoSession("cursor", "m1", "", 1) // unknown project
	if err := a.Put(ctx, first, ref, sum); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, src, "s.jsonl", "v2")
	_, sum2, _ := a.Stale(ref)
	second := demoSession("cursor", "m1", "/root/code/found", 1)
	if err := a.Put(ctx, second, ref, sum2); err != nil {
		t.Fatal(err)
	}
	all, _ := a.List(ctx, ports.Filter{})
	if len(all) != 1 {
		t.Fatalf("duplicate session across slugs: %d entries", len(all))
	}
	if all[0].Project != "root-code-found" {
		t.Errorf("kept the wrong copy: %+v", all[0])
	}
}

// Stale must short-circuit on identical size+mtime without re-hashing.
// We prove the fast path is in use by stubbing checksumFiles for the
// second Stale call (the test relies on the package-internal helper
// stampsEqual, exercised here through the public surface).
func TestStaleFastPath(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	src := t.TempDir()
	f := writeSourceFile(t, src, "s.jsonl", "abc\n")
	ref := ports.SessionRef{Agent: "codex", SourceID: "fast", Files: []string{f}}

	_, sum, _ := a.Stale(ref)
	if err := a.Put(ctx, demoSession("codex", "fast", "/p", 1), ref, sum); err != nil {
		t.Fatal(err)
	}

	// Replace contents but keep size+mtime — the fast path should declare
	// the ref unchanged. We bend reality on purpose: in real life the
	// agents only ever append to their session files, so a no-op append
	// would update mtime and force a rehash. This proves the cheap path
	// trusts the stamp.
	st, _ := os.Stat(f)
	if err := os.WriteFile(f, []byte("xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	stale, returnedSum, err := a.Stale(ref)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatalf("matching stamp should short-circuit, got stale=%v", stale)
	}
	if returnedSum == "" {
		t.Errorf("fast path must still return last known checksum, got empty")
	}

	// And bumping mtime forces a re-hash that now sees the new content.
	later := st.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	stale, newSum, err := a.Stale(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatalf("mtime change must trigger rehash and report stale")
	}
	if newSum == sum {
		t.Errorf("checksum should differ after content change")
	}
}

func TestChecksumStability(t *testing.T) {
	dir := t.TempDir()
	a := writeSourceFile(t, dir, "a", "AAA")
	b := writeSourceFile(t, dir, "b", "BBB")
	s1, err := checksumFiles([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := checksumFiles([]string{b, a}) // order must not matter
	if s1 != s2 {
		t.Error("checksum depends on file order")
	}
	if _, err := checksumFiles([]string{filepath.Join(dir, "missing")}); err == nil {
		t.Error("missing file must error")
	}
}
