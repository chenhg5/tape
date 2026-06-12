package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/tape/internal/core/ports"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func seedArchive(t *testing.T, dir string, files map[string]string) {
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

func TestPushInitCommitAndIdempotency(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	seedArchive(t, dir, map[string]string{"codex/p/s1/meta.json": `{"a":1}`})

	res, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: dir, Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed == 0 || res.Ref == "" {
		t.Errorf("first push: %+v", res)
	}
	if !strings.Contains(res.Note, "set --remote") {
		t.Errorf("no-remote note missing: %+v", res)
	}

	// second push with no changes must be a no-op, same HEAD
	res2, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed != 0 || res2.Ref != res.Ref {
		t.Errorf("idempotency violated: %+v vs %+v", res, res2)
	}
}

func TestPushDryRunCommitsNothing(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	seedArchive(t, dir, map[string]string{"a.json": "1"})

	res, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 1 || res.Ref != "" {
		t.Errorf("dry run result: %+v", res)
	}
	if out, _ := git(ctx, dir, "log", "--oneline"); strings.TrimSpace(out) != "" && !strings.Contains(out, "fatal") {
		t.Errorf("dry run created commits: %s", out)
	}
}

func TestPushPullRoundTripViaBareRemote(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	bare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := git(ctx, t.TempDir(), "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("bare init: %v %s", err, out)
	}

	src := t.TempDir()
	seedArchive(t, src, map[string]string{
		"codex/p/s1/meta.json":    `{"id":"s1"}`,
		"codex/p/s1/session.json": `{"messages":[]}`,
	})
	if _, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: src, Destination: bare}); err != nil {
		t.Fatal(err)
	}

	// fresh machine: clone into empty archive dir
	dst := filepath.Join(t.TempDir(), "archive")
	res, err := (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dst, Destination: bare})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "clone" {
		t.Errorf("action = %q", res.Action)
	}
	data, err := os.ReadFile(filepath.Join(dst, "codex/p/s1/meta.json"))
	if err != nil || string(data) != `{"id":"s1"}` {
		t.Errorf("cloned content: %q err=%v", data, err)
	}

	// subsequent pull (existing repo) takes the pull path
	if res, err = (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dst}); err != nil || res.Action != "pull" {
		t.Errorf("pull: %+v err=%v", res, err)
	}

	// remote persisted in repo config: a second push needs no --remote
	seedArchive(t, src, map[string]string{"codex/p/s2/meta.json": `{"id":"s2"}`})
	if res, err := (Target{}).Push(ctx, ports.BackupOpts{ArchiveDir: src}); err != nil || !strings.Contains(res.Note, "origin") {
		t.Errorf("second push should use stored remote: %+v err=%v", res, err)
	}
}

func TestPullErrorCases(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	// no repo, no remote
	if _, err := (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Error("want error for missing remote")
	}

	// non-empty non-repo dir must refuse to clone over data
	dir := t.TempDir()
	seedArchive(t, dir, map[string]string{"existing.json": "data"})
	_, err := (Target{}).Pull(ctx, ports.BackupOpts{ArchiveDir: dir, Destination: "ignored"})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("want not-empty refusal, got %v", err)
	}
}
