package cursor

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
	dir := t.TempDir()
	// Seed an unrelated workspace so schemaCheck() finds a real
	// reference and confirms the schema is well-formed before we
	// attempt our own write.
	buildStore(t, filepath.Join(dir, "deadbeefdeadbeefdeadbeefdeadbeef", "seed-sess"))

	s := &Source{dir: dir}
	ctx := context.Background()
	in := &model.Session{
		ID: "cursor/xyz", Agent: "cursor", SourceID: "xyz",
		Title: "ipc bug", CWD: "/root/code/demo", Model: "claude-fable-5",
		Messages: []model.Message{
			{Role: model.RoleUser, Text: "为什么 ipc 阻塞", Timestamp: time.Now().Add(-time.Minute)},
			{Role: model.RoleAssistant, Text: "channel 没被消费", Timestamp: time.Now()},
			{Role: model.RoleTool, Text: "tool noise must be skipped"},
		},
	}
	res, err := s.Write(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResumeCommand, "cursor-agent") || !strings.Contains(res.ResumeCommand, "/root/code/demo") {
		t.Errorf("resume cmd: %q", res.ResumeCommand)
	}
	if res.TargetFile == "" {
		t.Error("WriteResult.TargetFile should be set")
	}

	refs, err := s.List(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 refs (seed + restored), got %d", len(refs))
	}

	// Find the restored session (not the seeded one).
	var ours = ""
	for _, r := range refs {
		if r.SourceID != "seed-sess" {
			ours = r.SourceID
		}
	}
	if ours == "" {
		t.Fatal("restored session not in list")
	}
	var ref ports.SessionRef
	for _, r := range refs {
		if r.SourceID == ours {
			ref = r
		}
	}
	out, err := s.Load(ctx, ref)
	if err != nil || out == nil {
		t.Fatalf("load: out=%v err=%v", out, err)
	}
	if !strings.HasPrefix(out.Title, "[tape]") {
		t.Errorf("title should carry [tape]: %q", out.Title)
	}
	if out.Model != "claude-fable-5" {
		t.Errorf("model not round-tripped: %q", out.Model)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs (user + assistant), got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != model.RoleUser || !strings.Contains(out.Messages[0].Text, "为什么 ipc 阻塞") {
		t.Errorf("msg0 = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != model.RoleAssistant {
		t.Errorf("msg1 role = %v", out.Messages[1].Role)
	}
}

func TestWriteUnsupportedWhenChatsDirMissing(t *testing.T) {
	home := t.TempDir() // ~/.cursor/chats does NOT exist
	s := New(home)
	_, err := s.Write(context.Background(), &model.Session{
		Agent:    "cursor",
		CWD:      "/tmp",
		Messages: []model.Message{{Role: model.RoleUser, Text: "x"}},
	})
	if !errors.Is(err, ports.ErrNativeUnsupported) {
		t.Fatalf("want ErrNativeUnsupported, got %v", err)
	}
}

func TestWriteUnsupportedOnSchemaMismatch(t *testing.T) {
	// Build a store.db without the meta+blobs tables to simulate a
	// future cursor that reshuffled the schema; Write must bail.
	dir := t.TempDir()
	bad := filepath.Join(dir, "abc123", "sess1")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "store.db"), []byte("SQLite format 3\x00 not really"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Source{dir: dir}
	_, err := s.Write(context.Background(), &model.Session{
		Agent:    "cursor",
		CWD:      "/tmp",
		Messages: []model.Message{{Role: model.RoleUser, Text: "x"}},
	})
	if !errors.Is(err, ports.ErrNativeUnsupported) {
		t.Fatalf("want ErrNativeUnsupported on schema mismatch, got %v", err)
	}
}
