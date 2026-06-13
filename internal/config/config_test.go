package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadMissingReturnsZero: no config file = zero Config, no
// error. The CLI's flag-binding path leans on this so first-run
// users don't need to `tape config init` before using anything.
func TestLoadMissingReturnsZero(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c == nil || c.Defaults.Jobs != 0 || len(c.Defaults.ExcludeAgents) != 0 {
		t.Errorf("zero config violated: %+v", c)
	}
}

// TestSaveRoundTrip: Save → Load echoes every field, including
// the slice values.
func TestSaveRoundTrip(t *testing.T) {
	home := t.TempDir()
	c := &Config{Defaults: Defaults{
		ExcludeAgents: []string{"cursor", "opencode"},
		Jobs:          4,
		Format:        "zip",
	}}
	if err := Save(home, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home)); err != nil {
		t.Errorf("config file not written: %v", err)
	}
	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Defaults.Jobs != 4 || got.Defaults.Format != "zip" {
		t.Errorf("scalars: %+v", got.Defaults)
	}
	if len(got.Defaults.ExcludeAgents) != 2 {
		t.Errorf("slice: %+v", got.Defaults.ExcludeAgents)
	}
}

// TestGetSetUnsetByKey covers the reflection-driven dot-key API.
// Each branch matters: int, string, []string. Unknown keys must
// error, never silently no-op (typo → loss otherwise).
func TestGetSetUnsetByKey(t *testing.T) {
	c := &Config{}

	if err := Set(c, "defaults.jobs", "8"); err != nil {
		t.Fatal(err)
	}
	if c.Defaults.Jobs != 8 {
		t.Errorf("set jobs: %d", c.Defaults.Jobs)
	}

	if err := Set(c, "defaults.exclude_agents", "cursor, codex,opencode"); err != nil {
		t.Fatal(err)
	}
	if len(c.Defaults.ExcludeAgents) != 3 {
		t.Errorf("set slice: %v", c.Defaults.ExcludeAgents)
	}

	if err := Set(c, "defaults.format", "zip"); err != nil {
		t.Fatal(err)
	}

	// Get reports the value + whether it's non-zero. The zero-string
	// case is the most error-prone (mistaken for "unset"), so we
	// special-case it.
	v, set, err := Get(c, "defaults.jobs")
	if err != nil || v.(int) != 8 || !set {
		t.Errorf("get jobs: %v %v %v", v, set, err)
	}
	v, set, _ = Get(c, "defaults.compress")
	if set || v.(string) != "" {
		t.Errorf("compress should be unset: %v %v", v, set)
	}

	if err := Unset(c, "defaults.jobs"); err != nil {
		t.Fatal(err)
	}
	if c.Defaults.Jobs != 0 {
		t.Errorf("unset jobs: %d", c.Defaults.Jobs)
	}

	if err := Set(c, "defaults.wat", "1"); err == nil {
		t.Error("unknown key must error")
	}
	if err := Set(c, "defaults.jobs", "notanumber"); err == nil {
		t.Error("bad int must error")
	}
}

// TestKeysContainsEverySchemaLeaf: deleting / renaming a field is
// only obvious when this test fails. New fields are picked up
// automatically.
func TestKeysContainsEverySchemaLeaf(t *testing.T) {
	keys := Keys()
	want := []string{
		"defaults.compress", "defaults.exclude_agents",
		"defaults.exclude_dirs", "defaults.exclude_hosts",
		"defaults.format", "defaults.jobs",
	}
	got := strings.Join(keys, ",")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing key %q in %v", w, keys)
		}
	}
}

// TestSaveAtomic: Save uses temp + rename so a concurrent reader
// either sees the old or the new contents, never garbage. We
// can't easily reproduce that here, but at least verify the temp
// file is cleaned up.
func TestSaveAtomic(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, &Config{Defaults: Defaults{Jobs: 1}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover .tmp: %s", e.Name())
		}
	}
}
