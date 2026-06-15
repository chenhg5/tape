package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// Smoke tests: fast sanity checks that the binary starts, prints help and
// honors the output contract. They run in -short mode and are the first
// gate in CI.

func TestSmokeHelp(t *testing.T) {
	e := newEnv(t)
	r := e.mustRun(0, "--help")
	for _, want := range []string{"tape", "sync", "search", "restore", "export", "Exit codes"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("--help missing %q", want)
		}
	}
}

func TestSmokeVersion(t *testing.T) {
	e := newEnv(t)
	r := e.mustRun(0, "--version")
	if !strings.Contains(r.stdout, "tape") {
		t.Errorf("version output: %q", r.stdout)
	}
}

func TestSmokeUnknownCommand(t *testing.T) {
	e := newEnv(t)
	if r := e.run("frobnicate"); r.code != 2 {
		t.Errorf("unknown command: exit %d, want 2 (usage)", r.code)
	}
}

func TestSmokeUnknownFlag(t *testing.T) {
	e := newEnv(t)
	r := e.run("ls", "--frobnicate")
	if r.code != 2 {
		t.Errorf("unknown flag: exit %d, want 2 (usage)", r.code)
	}
	if r.errJSON(t)["error"] != "usage" {
		t.Errorf("error type = %v", r.errJSON(t))
	}
}

func TestSmokeWrongArgCount(t *testing.T) {
	e := newEnv(t)
	if r := e.run("show"); r.code != 2 {
		t.Errorf("missing arg: exit %d, want 2 (usage)", r.code)
	}
}

func TestSmokeSchemaIsValidJSON(t *testing.T) {
	e := newEnv(t)
	r := e.mustRun(0, "schema")
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Name        string `json:"name"`
			Subcommands []struct {
				Name string `json:"name"`
			} `json:"subcommands"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if envelope.Data.Name != "tape" {
		t.Errorf("root name = %q", envelope.Data.Name)
	}
	names := map[string]bool{}
	for _, c := range envelope.Data.Subcommands {
		names[c.Name] = true
	}
	// `index` is intentionally hidden — the user-facing surface is
	// `sync`, which self-heals the index. The hidden command still works
	// (see TestHiddenIndexRebuildStillRuns) but does not advertise.
	for _, want := range []string{"sync", "ls", "search", "show", "overview", "export", "restore", "schema"} {
		if !names[want] {
			t.Errorf("schema missing command %q", want)
		}
	}
	if names["index"] {
		t.Errorf("hidden command 'index' should not appear in schema listing")
	}
}

func TestSmokeSyncOnEmptyMachine(t *testing.T) {
	e := newEnv(t) // HOME has no agent data at all
	r := e.mustRun(0, "sync")
	d := r.data(t)
	if d["archived"] != float64(0) {
		t.Errorf("archived = %v", d["archived"])
	}
	// empty list is "no results": exit 3, but data still parseable
	r = e.mustRun(3, "ls")
	if r.data(t)["count"] != float64(0) {
		t.Errorf("ls count = %v", r.data(t)["count"])
	}
	if sessions, ok := r.data(t)["sessions"].([]any); !ok || len(sessions) != 0 {
		t.Errorf("empty sessions must be [] not null: %v", r.data(t)["sessions"])
	}
	if r.errJSON(t)["error"] != "no_results" {
		t.Errorf("stderr error = %v", r.errJSON(t))
	}
	// On a fresh machine the empty-archive hint must tell the user
	// to run `tape sync` — otherwise a first-time user gets a
	// cryptic "no results" and no path forward. The message and
	// suggestion are user-visible contract.
	if msg, _ := r.errJSON(t)["message"].(string); !strings.Contains(msg, "no archived sessions") {
		t.Errorf("empty-archive hint missing from message: %q", msg)
	}
	if sug, _ := r.errJSON(t)["suggestion"].(string); !strings.Contains(sug, "tape sync") {
		t.Errorf("empty-archive hint should suggest tape sync, got %q", sug)
	}
}

func TestSmokeSearchOnEmptyIndexHintsTapeSync(t *testing.T) {
	e := newEnv(t)
	// No sync yet: archive and index are both empty. Searching for a
	// term that exists in no session must give exit 3 *and* explain
	// that the index is empty + suggest `tape sync`. Before this fix
	// you just got "no results" with no path forward.
	r := e.mustRun(3, "search", "anything")
	stderr := r.errJSON(t)
	if stderr["error"] != "no_results" {
		t.Errorf("error type = %v", stderr["error"])
	}
	if msg, _ := stderr["message"].(string); !strings.Contains(msg, "index is empty") {
		t.Errorf("empty-index hint missing from message: %q", msg)
	}
	if sug, _ := stderr["suggestion"].(string); !strings.Contains(sug, "tape sync") {
		t.Errorf("empty-index hint should suggest tape sync, got %q", sug)
	}
}

func TestSmokeUsageErrors(t *testing.T) {
	e := newEnv(t)
	// missing required --to
	r := e.run("restore", "whatever")
	if r.code != 2 {
		t.Errorf("missing --to: exit %d, want 2", r.code)
	}
	if r.errJSON(t)["error"] != "usage" {
		t.Errorf("error type = %v", r.errJSON(t))
	}
	// bad --since
	if r := e.run("ls", "--since", "eleventy"); r.code != 2 {
		t.Errorf("bad --since: exit %d, want 2", r.code)
	}
}
