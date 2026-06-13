package e2e

import (
	"strings"
	"testing"
)

// TestCompletionEmitsScript: each shell flavor produces non-empty
// output and contains the recognizable header for that shell.
// We don't run the script — that would need a clean zsh/fish in
// the CI image — but a smoke-grep for the right boilerplate is
// enough to catch a cobra API regression.
func TestCompletionEmitsScript(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	cases := map[string]string{
		"bash":       "# bash completion",
		"zsh":        "#compdef tape",
		"fish":       "function __tape_",
		"powershell": "Register-ArgumentCompleter",
	}
	for shell, marker := range cases {
		r := e.mustRun(0, "completion", shell)
		if !strings.Contains(r.stdout, marker) {
			t.Errorf("completion %s missing marker %q\nfirst 200 chars: %s",
				shell, marker, head(r.stdout, 200))
		}
	}
	// Unknown shell is rejected — cobra's OnlyValidArgs maps to a
	// generic non-zero, the exact value (1 vs 2) varies by cobra
	// version. We just want "not 0".
	if r := e.run("completion", "elvish"); r.code == 0 {
		t.Errorf("unknown shell should be a non-zero exit, got 0")
	}
}

// TestConfigSetGetUnset: round-trips through the CLI, including
// the slice flavor (CSV input → JSON list output) and the unknown-
// key error path.
func TestConfigSetGetUnset(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.mustRun(0, "config", "set", "defaults.jobs", "4")
	e.mustRun(0, "config", "set", "defaults.exclude_agents", "cursor,opencode")

	d := e.mustRun(0, "config", "list", "--json").data(t)
	entries := d["entries"].([]any)
	got := map[string]any{}
	for _, raw := range entries {
		m := raw.(map[string]any)
		got[m["key"].(string)] = m["value"]
	}
	if got["defaults.jobs"].(float64) != 4 {
		t.Errorf("jobs: %v", got["defaults.jobs"])
	}
	if ex := got["defaults.exclude_agents"].([]any); len(ex) != 2 {
		t.Errorf("exclude_agents: %v", ex)
	}

	// Unset clears just that key.
	e.mustRun(0, "config", "unset", "defaults.jobs")
	g := e.mustRun(0, "config", "get", "defaults.jobs", "--json").data(t)
	if g["is_set"].(bool) {
		t.Errorf("jobs still set after unset: %v", g)
	}

	if r := e.run("config", "set", "defaults.wat", "1"); r.code != 2 {
		t.Errorf("unknown key should usage-error, got %d", r.code)
	}
}

// TestConfigDefaultsFeedExportFlags: if the user persists
// defaults.exclude_agents in config, an unflagged `tape export`
// still applies them — that's the whole point of the config
// surface.
func TestConfigDefaultsFeedExportFlags(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// Lock in "I never want codex chunks" via config; then export
	// split-by-agent must not produce a codex chunk even though
	// no --exclude-agent flag is on the command line.
	e.mustRun(0, "config", "set", "defaults.exclude_agents", "codex")
	d := e.mustRun(0, "export", "--split-by", "agent",
		"-o", e.dir+"/snap.tar.zst").data(t)
	for _, raw := range d["parts"].([]any) {
		p := raw.(map[string]any)
		if p["chunk"].(string) == "codex" {
			t.Errorf("config default ignored: codex chunk present in %v", p)
		}
	}
}

// TestHistoryRecordsSync: after one sync we should see one record
// in `tape history --json`. The contract for agent consumers is
// the same as for human users — same op identifier, same scope/
// counts keys.
func TestHistoryRecordsSync(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	d := e.mustRun(0, "history", "--json").data(t)
	records := d["records"].([]any)
	if len(records) < 1 {
		t.Fatalf("expected >=1 record, got %d", len(records))
	}
	var found bool
	for _, raw := range records {
		r := raw.(map[string]any)
		if r["op"].(string) != "sync" {
			continue
		}
		found = true
		counts := r["counts"].(map[string]any)
		if _, ok := counts["archived"]; !ok {
			t.Errorf("sync record missing archived count: %v", counts)
		}
		break
	}
	if !found {
		t.Errorf("no sync record found: %v", records)
	}
}

// TestUninstallDryRunListsSteps: the planner enumerates scheduler
// + archive + binary steps. We don't actually uninstall — that
// would nuke the e2e tempdir mid-test — but the dry-run path is
// where regression risk lives (silent skips, missing step types).
func TestUninstallDryRunListsSteps(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	r := e.run("uninstall", "--dry-run", "--json")
	if r.code != ExitDryRunOK() {
		t.Fatalf("dry-run exit = %d, want 10 (dry-run-ok)", r.code)
	}
	d := r.data(t)
	plan := d["plan"].([]any)
	kinds := map[string]bool{}
	for _, raw := range plan {
		kinds[raw.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["archive"] {
		t.Errorf("plan missing archive step: %+v", plan)
	}
	if !kinds["binary"] {
		t.Errorf("plan missing binary step: %+v", plan)
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ExitDryRunOK is the agreed-upon exit code for `--dry-run`
// successes (mirrors cli.ExitDryRunOK). Declared here so the e2e
// package doesn't import internal/cli — same value, separate copy.
func ExitDryRunOK() int { return 10 }
