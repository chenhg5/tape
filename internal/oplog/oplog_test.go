package oplog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAppendThenRead is the round-trip happy path: write three
// records and read them back in the order they were appended.
func TestAppendThenRead(t *testing.T) {
	home := t.TempDir()
	for i := 1; i <= 3; i++ {
		err := Append(home, Record{
			Op:      "sync",
			Started: time.Date(2026, 6, 13, 9, i, 0, 0, time.UTC),
			Counts:  map[string]int{"archived": i},
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	recs, err := Read(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d, want 3", len(recs))
	}
	for i, r := range recs {
		want := i + 1
		if r.Counts["archived"] != want {
			t.Errorf("record %d: archived=%d, want %d", i, r.Counts["archived"], want)
		}
	}
}

// TestAppendComputesDuration: callers don't set Duration; the
// package fills it from Started/Finished. Important because the
// CLI history view relies on it.
func TestAppendComputesDuration(t *testing.T) {
	home := t.TempDir()
	start := time.Now()
	err := Append(home, Record{
		Op:       "export",
		Started:  start,
		Finished: start.Add(1200 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := Read(home, 0)
	if recs[0].Duration != "1.2s" {
		t.Errorf("duration = %q, want 1.2s", recs[0].Duration)
	}
}

// TestReadMissingFileReturnsEmpty: no log = no error, no records.
// The CLI's history command relies on this for the empty-state UI.
func TestReadMissingFileReturnsEmpty(t *testing.T) {
	recs, err := Read(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected zero records, got %d", len(recs))
	}
}

// TestReadLimit returns the most recent N (slice end), matching
// the contract `tape history --limit` advertises.
func TestReadLimit(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 5; i++ {
		_ = Append(home, Record{
			Op: "sync", Started: time.Unix(int64(i), 0),
			Counts: map[string]int{"i": i},
		})
	}
	recs, _ := Read(home, 2)
	if len(recs) != 2 {
		t.Fatalf("limit 2: got %d", len(recs))
	}
	if recs[0].Counts["i"] != 3 || recs[1].Counts["i"] != 4 {
		t.Errorf("expected tail-2 (i=3,4), got i=%d,%d",
			recs[0].Counts["i"], recs[1].Counts["i"])
	}
}

// TestDisabledEnv kills Append silently when the env switch is
// on. The file must not exist afterward.
func TestDisabledEnv(t *testing.T) {
	t.Setenv(EnvDisable, "1")
	home := t.TempDir()
	if err := Append(home, Record{Op: "sync", Started: time.Now()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Errorf("disabled mode wrote a file: %v", err)
	}
}

// TestCorruptedTailDoesNotBreakRead: a half-written last line —
// possible after a SIGKILL mid-Append — should be tolerated.
// We write two good records, then append a partial JSON fragment
// without a newline, and Read still returns the two good ones.
func TestCorruptedTailDoesNotBreakRead(t *testing.T) {
	home := t.TempDir()
	_ = Append(home, Record{Op: "sync", Started: time.Unix(1, 0), Counts: map[string]int{"k": 1}})
	_ = Append(home, Record{Op: "sync", Started: time.Unix(2, 0), Counts: map[string]int{"k": 2}})
	f, err := os.OpenFile(Path(home), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"op":"export","star`) // truncated mid-key, no newline
	_ = f.Close()
	recs, err := Read(home, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2 (corrupt tail must be skipped)", len(recs))
	}
}

// TestPathSchema pins the on-disk layout so renaming would be a
// breaking change visible in tests.
func TestPathSchema(t *testing.T) {
	want := filepath.Join("/tape", "operations.log")
	if got := Path("/tape"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	if !strings.HasSuffix(FileName, ".log") {
		t.Errorf("FileName drift: %q", FileName)
	}
}
