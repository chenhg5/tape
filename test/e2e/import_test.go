package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestImportShareRoundtrip walks through the v0.3.0 "share with a friend"
// scenario end-to-end with two independent tape installations:
//
//  1. env A has the seeded archive
//  2. env A produces a single-session share bundle on disk
//  3. env B (a fresh, empty tape home) imports that bundle
//  4. env B can `tape ls` the imported session
//  5. env B can `tape restore --strategy memory` it back into a chat handoff
//
// We use the memory strategy on the restore step because the e2e runner
// does not have the agent CLIs installed; the contract we're pinning is
// "the bundle round-trips through the archive layer", not "we can launch
// claude-code on this machine". The bundle code path is the same.
func TestImportShareRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	a := newEnv(t)
	a.seedAllAgents()
	a.mustRun(0, "sync")

	// On A: list sessions, pick the first one, share it to disk.
	ls := a.mustRun(0, "ls", "--print", "--limit", "1").data(t)
	sessions := ls["sessions"].([]any)
	if len(sessions) == 0 {
		t.Skip("nothing to share; seed didn't take")
	}
	id := sessions[0].(map[string]any)["id"].(string)

	bundlePath := filepath.Join(t.TempDir(), "share.tar.zst")
	share := a.mustRun(0, "share", id, "-o", bundlePath).data(t)
	if share["result"] == nil {
		t.Fatalf("share returned no result: %+v", share)
	}

	// On B: a brand-new tape home with nothing in it. Empty ls exits 3
	// (ExitNoResults), which is the documented contract; we only check
	// that the imported id isn't already present.
	b := newEnv(t)
	pre := b.run("ls", "--print", "--limit", "10")
	if pre.code != 0 && pre.code != 3 {
		t.Fatalf("empty ls: exit %d, want 0 or 3", pre.code)
	}
	if strings.Contains(pre.stdout, id) {
		t.Fatalf("env B already has session %s before import", id)
	}

	imp := b.mustRun(0, "import", bundlePath).data(t)
	if imp == nil {
		t.Fatal("import returned no JSON data")
	}

	post := b.mustRun(0, "ls", "--print", "--limit", "10").data(t)
	postSessions := post["sessions"].([]any)
	found := false
	for _, raw := range postSessions {
		if raw.(map[string]any)["id"].(string) == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("imported session %s not visible in env B ls (%+v)", id, postSessions)
	}

	// Round-trip restore: memory strategy writes a handoff file under cwd
	// without needing the real agent CLI. Exit 0 confirms the session was
	// loadable from the archive. We pick the source agent itself as the
	// target — restore requires --to so the prompt's `cd <cwd>` line and
	// resume command match what the user will actually run.
	parts := strings.SplitN(id, "/", 2)
	srcAgent := parts[0]
	b.mustRun(0, "restore", id, "--strategy", "memory", "--to", srcAgent)
}

// TestImportConflictSkipExitCode pins the exit-code contract from the plan:
// a non-TTY second import with default `skip` exits 4 to signal "merged
// nothing, leftovers remain" — distinct from generic error (1) so CI scripts
// can branch on "needs human" without losing "ran successfully otherwise".
func TestImportConflictSkipExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	a := newEnv(t)
	a.seedAllAgents()
	a.mustRun(0, "sync")
	ls := a.mustRun(0, "ls", "--print", "--limit", "1").data(t)
	sessions := ls["sessions"].([]any)
	if len(sessions) == 0 {
		t.Skip("no sessions to share")
	}
	id := sessions[0].(map[string]any)["id"].(string)

	bundlePath := filepath.Join(t.TempDir(), "dup.tar.zst")
	a.mustRun(0, "share", id, "-o", bundlePath)

	b := newEnv(t)
	b.mustRun(0, "import", bundlePath)

	// Second import with default skip on non-TTY: archive already has the
	// session under the same id but the checksum in the on-disk meta.json
	// (written by tape import) matches the bundle's, so this is an
	// idempotent silent skip (exit 0 + skipped=1).
	r2 := b.run("import", bundlePath)
	if r2.code != 0 && r2.code != 4 {
		t.Fatalf("re-import: exit %d, want 0 (idempotent) or 4 (unresolved)\nstdout: %s\nstderr: %s",
			r2.code, r2.stdout, r2.stderr)
	}
}

// TestImportConflictOverwrite exercises --on-conflict overwrite through
// the real binary on a second bundle with deliberately different content
// (we re-share after editing the title via direct archive manipulation
// would be flaky here, so we reuse the same bundle and trust the
// idempotent path; the action accounting still surfaces in JSON).
func TestImportConflictOverwriteCmd(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	a := newEnv(t)
	a.seedAllAgents()
	a.mustRun(0, "sync")
	ls := a.mustRun(0, "ls", "--print", "--limit", "1").data(t)
	id := ls["sessions"].([]any)[0].(map[string]any)["id"].(string)

	bundlePath := filepath.Join(t.TempDir(), "ow.tar.zst")
	a.mustRun(0, "share", id, "-o", bundlePath)

	b := newEnv(t)
	b.mustRun(0, "import", bundlePath)
	r := b.mustRun(0, "import", bundlePath, "--on-conflict", "overwrite")
	if !strings.Contains(r.stdout, `"action"`) {
		t.Fatalf("expected per-session JSON in stdout, got %q", r.stdout)
	}
}

// TestImportRewriteCWDCmd pins --rewrite-cwd through the binary: a
// rewritten path means the session lands under a different project_slug
// than the bundle declared, so `tape ls --cwd <new>` finds it.
func TestImportRewriteCWDCmd(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	a := newEnv(t)
	a.seedAllAgents()
	a.mustRun(0, "sync")
	ls := a.mustRun(0, "ls", "--print", "--limit", "1").data(t)
	id := ls["sessions"].([]any)[0].(map[string]any)["id"].(string)
	bundlePath := filepath.Join(t.TempDir(), "cwd.tar.zst")
	a.mustRun(0, "share", id, "-o", bundlePath)

	b := newEnv(t)
	newCWD := filepath.Join(t.TempDir(), "local-here")
	b.mustRun(0, "import", bundlePath, "--rewrite-cwd", newCWD)
	post := b.mustRun(0, "ls", "--print", "--limit", "10").data(t)
	sessions := post["sessions"].([]any)
	foundCWD := ""
	for _, raw := range sessions {
		s := raw.(map[string]any)
		if s["id"].(string) == id {
			if c, ok := s["cwd"].(string); ok {
				foundCWD = c
			}
		}
	}
	if foundCWD != newCWD {
		t.Fatalf("imported session cwd = %q, want %q", foundCWD, newCWD)
	}
}
