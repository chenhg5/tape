package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestRoundTrip(t *testing.T) {
	m := &Manifest{
		Kind:        KindShare,
		TapeVersion: "0.3.0",
		Source:      SourceInfo{Host: "alpha", TapeHome: "/root/.tape"},
		Sessions: []SessionRow{
			{ID: "claude-code/abc", Agent: "claude-code", SourceID: "abc",
				ProjectSlug: "root-code-tape", OriginalCWD: "/root/code/tape",
				Title: "auth refactor", MsgCount: 47,
				StartedAt: time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC),
				Checksum:  "sha256:abc",
			},
		},
	}
	var buf bytes.Buffer
	if err := Write(&buf, m); err != nil {
		t.Fatal(err)
	}
	// Write defaults missing fields in-place.
	if m.SchemaVersion != SchemaV1 {
		t.Errorf("schema_version default: %d", m.SchemaVersion)
	}
	if m.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set by Write")
	}

	// Indented output: there's a newline+space after the leading brace.
	if !strings.Contains(buf.String(), "\n  \"schema_version\"") {
		t.Errorf("Write should indent JSON, got:\n%s", buf.String())
	}

	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaV1 {
		t.Errorf("schema_version round-trip: %d", got.SchemaVersion)
	}
	if got.Kind != KindShare || got.TapeVersion != "0.3.0" {
		t.Errorf("kind/tape_version: %+v", got)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions: %d", len(got.Sessions))
	}
	row := got.Sessions[0]
	if row.ID != "claude-code/abc" || row.MsgCount != 47 {
		t.Errorf("session row: %+v", row)
	}
}

func TestReadMissingSchemaVersionIsSentinel(t *testing.T) {
	// Bundle from a v0.2.x tape: well-formed JSON but no
	// schema_version key. Importer relies on this to fall back to
	// layout-scan mode rather than blindly trusting the file.
	body := `{"tape_version":"0.2.0","sessions":[]}`
	_, err := Read(strings.NewReader(body))
	if !errors.Is(err, ErrMissingSchemaVersion) {
		t.Fatalf("want ErrMissingSchemaVersion, got %v", err)
	}
}

func TestReadFutureSchemaIsSentinel(t *testing.T) {
	body := `{"schema_version":99,"tape_version":"9.9.9","sessions":[]}`
	_, err := Read(strings.NewReader(body))
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("want ErrUnsupportedSchema, got %v", err)
	}
}

func TestReadInvalidJSON(t *testing.T) {
	_, err := Read(strings.NewReader("not json"))
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestRowFromSessionCopiesKeyFields(t *testing.T) {
	s := &model.Session{
		ID: "codex/xyz", Agent: "codex", SourceID: "xyz",
		Title: "t", CWD: "/root/code/demo",
		StartedAt: time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC),
		Messages:  []model.Message{{Role: model.RoleUser, Text: "hi"}},
	}
	row := RowFromSession(s, "sha256:zzz")
	if row.ID != "codex/xyz" || row.ProjectSlug != "root-code-demo" || row.Checksum != "sha256:zzz" {
		t.Errorf("row = %+v", row)
	}
	if row.MsgCount != 1 {
		t.Errorf("msg count = %d", row.MsgCount)
	}
}

func TestSerializedShapeMatchesPlan(t *testing.T) {
	// Belt-and-suspenders: assert the on-disk JSON has the exact
	// field names the import side reads. Drift here breaks
	// cross-version interop, which is the whole point of the manifest.
	m := &Manifest{
		Kind:        KindShare,
		TapeVersion: "0.3.0",
		Source:      SourceInfo{Host: "h"},
		Sessions:    []SessionRow{{ID: "a/b", Agent: "a", SourceID: "b"}},
	}
	var buf bytes.Buffer
	if err := Write(&buf, m); err != nil {
		t.Fatal(err)
	}
	var anyMap map[string]any
	if err := json.Unmarshal(buf.Bytes(), &anyMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "tape_version", "kind", "created_at", "source", "sessions"} {
		if _, ok := anyMap[key]; !ok {
			t.Errorf("manifest missing %q field; on-disk shape changed", key)
		}
	}
}
