package iflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectAndLoadViaGeminiSchema(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".iflow", "projects", "abc123")
	_ = os.MkdirAll(projDir, 0o700)
	p := filepath.Join(projDir, "session-2026-06-12T10-00-d7501ec6.jsonl")
	lines := []string{
		`{"sessionId":"d7501ec6","projectHash":"abc123","startTime":"2026-06-12T10:00:00.000Z","directories":["/root/code/demo"]}`,
		`{"id":"m1","type":"user","content":"hello iflow","timestamp":"2026-06-12T10:00:01.000Z"}`,
		`{"id":"m2","type":"gemini","content":"hi","timestamp":"2026-06-12T10:00:02.000Z","model":"qwen-plus"}`,
	}
	_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	src := New(dir)
	if found, _, _ := src.Detect(context.Background()); !found {
		t.Fatal("Detect should find ~/.iflow")
	}
	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs: %d want 1", len(refs))
	}
	if refs[0].Agent != "iflow" {
		t.Errorf("agent slug: %s", refs[0].Agent)
	}

	s, err := src.Load(context.Background(), refs[0])
	if err != nil || s == nil {
		t.Fatalf("Load: %v session=%v", err, s)
	}
	if s.Agent != "iflow" {
		t.Errorf("session agent must be relabelled to iflow, got %q", s.Agent)
	}
	if s.ID != "iflow/"+refs[0].SourceID {
		t.Errorf("session id rewriting wrong: %q", s.ID)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("messages: %d", len(s.Messages))
	}
	if s.Messages[0].Text != "hello iflow" {
		t.Errorf("first message: %q", s.Messages[0].Text)
	}
}
