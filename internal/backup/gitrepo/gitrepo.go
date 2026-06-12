// Package gitrepo backs up the archive by turning the archive directory
// itself into a git repository: push = commit (+ push to origin when set),
// pull = pull/clone. The remote URL lives in the repo's own git config, so
// tape needs no extra state.
package gitrepo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/ports"
)

type Target struct{}

func (Target) Name() string { return "git" }

func (t Target) Push(ctx context.Context, opts ports.BackupOpts) (*ports.BackupResult, error) {
	dir := opts.ArchiveDir
	if err := ensureGit(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if !isRepo(dir) {
		if _, err := git(ctx, dir, "init", "-b", "main"); err != nil {
			return nil, err
		}
	}
	if opts.Destination != "" {
		if hasRemote(ctx, dir) {
			if _, err := git(ctx, dir, "remote", "set-url", "origin", opts.Destination); err != nil {
				return nil, err
			}
		} else if _, err := git(ctx, dir, "remote", "add", "origin", opts.Destination); err != nil {
			return nil, err
		}
	}

	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	changed := 0
	if status != "" {
		changed = len(strings.Split(strings.TrimRight(status, "\n"), "\n"))
	}
	res := &ports.BackupResult{Target: "git", Action: "push", Changed: changed}

	if opts.DryRun {
		res.Note = "dry run: nothing committed"
		return res, nil
	}
	if changed > 0 {
		if _, err := git(ctx, dir, "add", "-A"); err != nil {
			return nil, err
		}
		msg := opts.Message
		if msg == "" {
			msg = fmt.Sprintf("tape backup %s", time.Now().UTC().Format(time.RFC3339))
		}
		if _, err := git(ctx, dir, "-c", "user.name=tape", "-c", "user.email=tape@local", "commit", "-m", msg); err != nil {
			return nil, err
		}
	}
	ref, _ := git(ctx, dir, "rev-parse", "--short", "HEAD")
	res.Ref = strings.TrimSpace(ref)

	if hasRemote(ctx, dir) {
		if out, err := git(ctx, dir, "push", "-u", "origin", "main"); err != nil {
			return nil, fmt.Errorf("push: %w: %s", err, out)
		}
		res.Note = "pushed to origin"
	} else {
		res.Note = "committed locally; set --remote to push off-machine"
	}
	return res, nil
}

func (t Target) Pull(ctx context.Context, opts ports.BackupOpts) (*ports.BackupResult, error) {
	dir := opts.ArchiveDir
	if err := ensureGit(); err != nil {
		return nil, err
	}
	if !isRepo(dir) {
		if opts.Destination == "" {
			return nil, fmt.Errorf("archive is not a git repo and no --remote given")
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return nil, err
		}
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			return nil, fmt.Errorf("archive dir %s is not empty and not a git repo; move it aside first", dir)
		}
		if out, err := git(ctx, filepath.Dir(dir), "clone", opts.Destination, dir); err != nil {
			return nil, fmt.Errorf("clone: %w: %s", err, out)
		}
		return &ports.BackupResult{Target: "git", Action: "clone", Note: "cloned " + opts.Destination}, nil
	}
	if out, err := git(ctx, dir, "pull", "--ff-only", "origin", "main"); err != nil {
		return nil, fmt.Errorf("pull: %w: %s", err, out)
	}
	return &ports.BackupResult{Target: "git", Action: "pull"}, nil
}

func ensureGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git binary not found in PATH")
	}
	return nil
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func hasRemote(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "remote")
	return err == nil && strings.Contains(out, "origin")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}
