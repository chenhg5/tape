package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

func TestWriteReadBack(t *testing.T) {
	home := t.TempDir()
	seedDB(t, home) // installs an opencode-shaped DB the writer can extend
	s := New(home)
	ctx := context.Background()
	in := &model.Session{
		ID: "opencode/xyz", Agent: "opencode", SourceID: "xyz",
		Title: "auth refactor", CWD: "/root/code/demo",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "请把 session 换成 jwt", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "好,先读 auth.go", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "opencode") || !strings.Contains(res.ResumeCommand, "/root/code/demo") {
		t.Errorf("resume cmd: %q", res.ResumeCommand)
	}
	if res.TargetFile == "" {
		t.Error("WriteResult.TargetFile should be set")
	}

	refs, err := s.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// seedDB created 1 top-level session; ours brings it to 2.
	if len(refs) != 2 {
		t.Fatalf("want 2 refs (seed + restored), got %d", len(refs))
	}
	// Find the one we just wrote (the seeded session has id "ses_top").
	var ours ports.SessionRef
	for _, r := range refs {
		if r.SourceID != "ses_top" {
			ours = r
			break
		}
	}
	if ours.SourceID == "" {
		t.Fatal("restored session not found in list")
	}
	out, err := s.Load(ctx, ours)
	if err != nil || out == nil {
		t.Fatalf("load: out=%v err=%v", out, err)
	}
	if !strings.HasPrefix(out.Title, "[tape]") {
		t.Errorf("title should carry [tape]: %q", out.Title)
	}
	if out.CWD != "/root/code/demo" {
		t.Errorf("cwd not round-tripped: %q", out.CWD)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs (user + assistant; tool skipped), got %d: %+v",
			len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != model.RoleUser || !strings.Contains(out.Messages[0].Text, "请把 session 换成 jwt") {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("msg1 role = %v", out.Messages[1].Role)
	}
}

func TestWriteUnsupportedWhenDBMissing(t *testing.T) {
	// No seedDB → no opencode.db on disk. Write must surface the
	// sentinel so restore can fall back gracefully.
	home := t.TempDir()
	s := New(home)
	_, err := s.Write(context.Background(), &model.Session{
		Agent: "opencode", CWD: home,
		Messages: []model.Message{{Role: model.RoleUser, Text: "x"}},
	})
	if !errors.Is(err, ports.ErrNativeUnsupported) {
		t.Fatalf("want ErrNativeUnsupported, got %v", err)
	}
}

func TestWriteUnsupportedWhenSchemaMismatched(t *testing.T) {
	// Touch a file at the expected DB path but skip the schema setup —
	// looks like a DB to Detect() (and us) but isn't usable.
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "opencode")
	_ = (func() error {
		return nil
	})() // keep mkdir local-only
	bogus := filepath.Join(dir, "opencode.db")
	if err := writeRandomBytes(t, bogus); err != nil {
		t.Skip("can't seed bogus DB:", err)
	}
	s := New(home)
	_, err := s.Write(context.Background(), &model.Session{
		Agent: "opencode", CWD: "/tmp",
		Messages: []model.Message{{Role: model.RoleUser, Text: "x"}},
	})
	if !errors.Is(err, ports.ErrNativeUnsupported) {
		t.Fatalf("want ErrNativeUnsupported on schema mismatch, got %v", err)
	}
}

func writeRandomBytes(t *testing.T, path string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("not a sqlite file"), 0o600)
}
