package qoder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectAndLoadViaQwenSchema(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, ".qoder", "projects", "rootcode-demo")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sid := "s-001"
	lines := []string{
		`{"uuid":"u1","parentUuid":null,"sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:00.000Z","type":"user","cwd":"/root/code/demo","gitBranch":"main","message":{"role":"user","parts":[{"text":"用 qoder 重写 hello.go"}]}}`,
		`{"uuid":"a1","parentUuid":"u1","sessionId":"` + sid + `","timestamp":"2026-06-12T10:00:01.000Z","type":"assistant","model":"qoder-pro","message":{"role":"model","parts":[{"text":"已重写为 Go 1.26 idiomatic 版本"}]}}`,
	}
	jsonlPath := filepath.Join(projDir, sid+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Sidecar metadata that List() must filter out.
	_ = os.WriteFile(filepath.Join(projDir, sid+"-session.json"), []byte(`{}`), 0o600)

	src := New(home)
	if found, _, _ := src.Detect(context.Background()); !found {
		t.Fatal("Detect should find ~/.qoder/projects")
	}
	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs (sidecar should be excluded)", len(refs))
	}
	if refs[0].Agent != "qoder" {
		t.Errorf("agent slug: %s", refs[0].Agent)
	}
	if refs[0].SourceID != sid {
		t.Errorf("sourceID: %s", refs[0].SourceID)
	}

	s, err := src.Load(context.Background(), refs[0])
	if err != nil || s == nil {
		t.Fatalf("Load: %v session=%v", err, s)
	}
	if s.Agent != "qoder" || s.ID != "qoder/"+sid {
		t.Errorf("session relabel wrong: agent=%s id=%s", s.Agent, s.ID)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("messages: %d", len(s.Messages))
	}
	if s.Messages[0].Text != "用 qoder 重写 hello.go" {
		t.Errorf("first message: %q", s.Messages[0].Text)
	}
	if s.CWD != "/root/code/demo" {
		t.Errorf("cwd: %q", s.CWD)
	}
}

func TestListExcludesSidecarOnly(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, ".qoder", "projects", "demo")
	_ = os.MkdirAll(projDir, 0o700)
	// only sidecar present, no transcript
	_ = os.WriteFile(filepath.Join(projDir, "x-session.json"), []byte(`{}`), 0o600)
	_ = os.WriteFile(filepath.Join(projDir, "x-session.jsonl"), []byte(`{}`), 0o600) // sidecar masquerading as jsonl

	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})
	if len(refs) != 0 {
		t.Fatalf("sidecar-only project should yield no refs, got %+v", refs)
	}
}
