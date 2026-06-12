package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Search filters must compose with the query end to end.
func TestSearchFilters(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// all three fixture sessions mention their topic only; "demo" project
	// is shared, so --project narrows nothing but must not break
	d := e.mustRun(0, "search", "构建速度", "--agent", "codex").data(t)
	if d["count"] != float64(1) {
		t.Errorf("agent filter: %v", d["count"])
	}
	// wrong agent: no hits, exit 3
	e.mustRun(3, "search", "构建速度", "--agent", "cursor")

	d = e.mustRun(0, "search", "迁移", "--project", "/root/code/demo").data(t)
	if d["count"].(float64) < 1 {
		t.Errorf("project filter: %v", d["count"])
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

// The index is derived state: wiping it must be fully recoverable.
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

	d := e.mustRun(0, "index", "rebuild").data(t)
	if d["indexed"] != float64(3) {
		t.Fatalf("rebuild: %v", d)
	}
	e.mustRun(0, "search", "stateless")
}

// --dir must override TAPE_DIR, keeping installations isolated.
func TestDirFlagOverride(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	other := t.TempDir()

	e.mustRun(0, "sync", "--dir", other)
	// default TAPE_DIR stayed empty
	e.mustRun(3, "ls")
	// the override location has the data
	d := e.mustRun(0, "ls", "--dir", other).data(t)
	if d["count"] != float64(3) {
		t.Errorf("ls --dir: %v", d["count"])
	}
	if entries, _ := os.ReadDir(filepath.Join(other, "archive")); len(entries) == 0 {
		t.Error("override dir has no archive")
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
