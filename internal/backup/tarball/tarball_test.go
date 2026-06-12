package tarball

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/chenhg5/tape/internal/core/ports"
)

func seed(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	seed(t, src, map[string]string{
		"codex/p/s1/session.json": `{"text":"会话内容"}`,
		"codex/p/s1/raw/x.jsonl":  "raw line",
		".git/config":             "must be skipped",
	})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	res, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: src, Destination: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 {
		t.Errorf(".git not skipped or wrong count: %+v", res)
	}

	dst := t.TempDir()
	res, err = (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dst, Destination: out})
	if err != nil || res.Changed != 2 {
		t.Fatalf("import: %+v err=%v", res, err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "codex/p/s1/session.json"))
	if err != nil || string(data) != `{"text":"会话内容"}` {
		t.Errorf("content mismatch: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git", "config")); err == nil {
		t.Error(".git leaked into artifact")
	}
}

func TestExportAppliesRedaction(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	seed(t, src, map[string]string{"s/session.json": "key=AKIAIOSFODNN7EXAMPLE end"})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	_, err := (Target{}).Push(ctx, ports.BackupOpts{
		ArchiveDir: src, Destination: out,
		RedactCopy: func(path string, data []byte) []byte {
			return bytes.ReplaceAll(data, []byte("AKIAIOSFODNN7EXAMPLE"), []byte("[GONE]"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// local file untouched
	local, _ := os.ReadFile(filepath.Join(src, "s/session.json"))
	if !strings.Contains(string(local), "AKIA") {
		t.Error("local file was modified")
	}
	// artifact redacted
	dst := t.TempDir()
	if _, err := (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dst, Destination: out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "s/session.json"))
	if strings.Contains(string(got), "AKIA") || !strings.Contains(string(got), "[GONE]") {
		t.Errorf("artifact not redacted: %q", got)
	}
}

func TestExportDryRun(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	seed(t, src, map[string]string{"a": "1", "b": "2"})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	res, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: src, Destination: out, DryRun: true})
	if err != nil || res.Changed != 2 {
		t.Fatalf("dry run: %+v err=%v", res, err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("dry run wrote the artifact")
	}
}

func TestPushRequiresOutput(t *testing.T) {
	if _, err := (Target{}).Push(context.Background(), ports.BackupOpts{ArchiveDir: t.TempDir()}); err == nil {
		t.Error("missing destination must error")
	}
}

// --since produces a smaller, incremental tarball containing only sessions
// updated within the window.
func TestExportIncrementalSince(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()

	// "old" session: updated 3 days ago; "new": now
	seed(t, src, map[string]string{
		"codex/p/old/session.json": `{"hi":"old"}`,
		"codex/p/old/raw/x.jsonl":  "old",
		"codex/p/old/meta.json":    `{"summary":{"updated_at":"2025-01-01T00:00:00Z"}}`,
		"codex/p/new/session.json": `{"hi":"new"}`,
		"codex/p/new/raw/y.jsonl":  "new",
		"codex/p/new/meta.json":    `{"summary":{"updated_at":"2099-01-01T00:00:00Z"}}`,
	})
	out := filepath.Join(t.TempDir(), "incr.tar.zst")

	since := time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := (Target{}).Push(ctx, ports.BackupOpts{
		ArchiveDir: src, Destination: out, Since: since,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "export-incremental" {
		t.Errorf("action = %q, want export-incremental", res.Action)
	}
	// only the new session's three files (session/meta/raw) should travel
	if res.Changed != 3 {
		t.Errorf("incremental count = %d, want 3", res.Changed)
	}

	// extract and confirm: only new/* is present
	dst := t.TempDir()
	if _, err := (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dst, Destination: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "codex/p/new/session.json")); err != nil {
		t.Errorf("new session missing from incremental tar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "codex/p/old/session.json")); err == nil {
		t.Errorf("old session leaked into incremental tar")
	}
}

// A malicious artifact must not write outside the archive dir.
func TestImportBlocksPathTraversal(t *testing.T) {
	evil := filepath.Join(t.TempDir(), "evil.tar.zst")
	f, err := os.Create(evil)
	if err != nil {
		t.Fatal(err)
	}
	zw, _ := zstd.NewWriter(f)
	tw := tar.NewWriter(zw)
	payload := []byte("pwned")
	tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0o600, Size: int64(len(payload))})
	tw.Write(payload)
	tw.Close()
	zw.Close()
	f.Close()

	dst := filepath.Join(t.TempDir(), "archive")
	_, err = (Target{}).Pull(context.Background(), ports.BackupOpts{ArchiveDir: dst, Destination: evil})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("traversal not blocked: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dst), "escape.txt")); statErr == nil {
		t.Error("file escaped the archive dir")
	}
}
