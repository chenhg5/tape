package e2e

import (
	"testing"
)

// TestVersionJSONHasEveryField: tape version is run from a pipe in
// tests (no TTY), so it auto-downgrades to --json. We assert the
// JSON contract everyone — scripts and `tape update` — depends on.
// Field set drifts are caught here before anyone parses against
// them.
func TestVersionJSONHasEveryField(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)

	d := e.mustRun(0, "version", "--json").data(t)
	for _, want := range []string{"version", "go", "os", "arch", "install", "tape_home"} {
		if _, ok := d[want]; !ok {
			t.Errorf("json version missing key %q: %v", want, d)
		}
	}
	// install must be one of the known enum values; an empty string
	// would mean detectInstall silently failed (regression risk).
	install, _ := d["install"].(string)
	allowed := map[string]bool{"npm": true, "go-install": true, "homebrew": true, "manual": true}
	if !allowed[install] {
		t.Errorf("install enum = %q, want one of npm/go-install/homebrew/manual", install)
	}
}

// TestUpdateCheckOffline tries `tape update --check` and tolerates
// either "up to date" / "newer release" exit codes or a network
// failure (CI runners are sometimes air-gapped). The important
// invariant is "this command exists and emits parseable JSON
// describing what would happen".
func TestUpdateCheckOffline(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	r := e.run("update", "--check", "--json")
	switch r.code {
	case 0, 1:
		// 0 = up to date, 1 = generic failure (no network, no
		// releases yet). Both are acceptable in CI; we only check
		// that some structured JSON came out on the success path.
		if r.code == 0 {
			data := r.data(t)
			for _, want := range []string{"current", "latest", "up_to_date", "channel"} {
				if _, ok := data[want]; !ok {
					t.Errorf("update --check json missing %q: %v", want, data)
				}
			}
		}
	default:
		t.Errorf("update --check: unexpected exit %d (stderr=%s)", r.code, r.stderr)
	}
}

// TestSyncStatusNoSchedulerYet: a fresh env should report no
// scheduled sync. We don't actually install one (that would
// touch ~/.config / launchd) — the CLI plumbing is what's at risk
// of breaking, not the platform integration.
func TestSyncStatusNoSchedulerYet(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	r := e.mustRun(0, "sync", "--status", "--json")
	d := r.data(t)
	status, ok := d["status"].(map[string]any)
	if !ok {
		t.Fatalf("status payload: %v", d)
	}
	if installed, _ := status["installed"].(bool); installed {
		// shared CI box may already have one — only fail if the
		// schema is wrong, not if a prior run left it.
		t.Logf("scheduler reports installed (likely a prior run); status=%v", status)
	}
	if _, ok := status["backend"].(string); !ok {
		t.Errorf("status missing backend: %v", status)
	}
}
