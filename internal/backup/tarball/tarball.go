// Package tarball exports the archive as a single .tar.zst file. Because the
// tarball is a generated artifact (unlike the in-place git repo), contents
// can be redacted in-stream on the way in; local files stay untouched.
package tarball

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/chenhg5/tape/internal/core/ports"
)

type Target struct{}

func (Target) Name() string { return "tar" }

func (t Target) Push(ctx context.Context, opts ports.BackupOpts) (*ports.BackupResult, error) {
	out := opts.Destination
	if out == "" {
		return nil, fmt.Errorf("tar target needs --output <file.tar.zst>")
	}
	// keep is used to scope the export. When opts.Since is non-zero,
	// we include only session directories whose meta.json shows
	// updated_at >= Since; this gives users a cheap incremental snapshot.
	keep, err := buildKeepFilter(opts.ArchiveDir, opts.Since)
	if err != nil {
		return nil, err
	}
	// pre-scan once so progress has a denominator and dry runs are cheap
	var total int64
	if err := walkFiles(opts.ArchiveDir, func(_, rel string) error {
		if keep(rel) {
			total++
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if opts.DryRun {
		note := fmt.Sprintf("dry run: would write %d file(s) to %s", total, out)
		if !opts.Since.IsZero() {
			note += fmt.Sprintf(" (incremental since %s)", opts.Since.UTC().Format(time.RFC3339))
		}
		return &ports.BackupResult{Target: "tar", Action: "export", Changed: int(total), Note: note}, nil
	}
	count := 0

	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)

	err = walkFiles(opts.ArchiveDir, func(path, rel string) error {
		if !keep(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if opts.RedactCopy != nil {
			data = opts.RedactCopy(rel, data)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: rel, Mode: 0o600, Size: int64(len(data)), ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		count++
		if opts.OnProgress != nil {
			opts.OnProgress(int64(count), total, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	action := "export"
	note := ""
	if !opts.Since.IsZero() {
		action = "export-incremental"
		note = fmt.Sprintf("included sessions updated since %s", opts.Since.UTC().Format(time.RFC3339))
	}
	return &ports.BackupResult{Target: "tar", Action: action, Changed: count, Ref: out, Note: note}, nil
}

// buildKeepFilter returns a predicate that reports whether a relative path
// from the archive root should be included in the tarball. When since is
// zero the filter is identity (include everything). Otherwise we walk the
// archive once, read every meta.json, and keep only paths under sessions
// whose updated_at >= since.
func buildKeepFilter(root string, since time.Time) (func(rel string) bool, error) {
	if since.IsZero() {
		return func(string) bool { return true }, nil
	}
	keep := map[string]struct{}{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "meta.json" {
			if d != nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil // ignore broken entries
		}
		var m struct {
			Summary struct {
				UpdatedAt time.Time `json:"updated_at"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil
		}
		if m.Summary.UpdatedAt.Before(since) {
			return nil
		}
		dir, err := filepath.Rel(root, filepath.Dir(p))
		if err == nil {
			keep[filepath.ToSlash(dir)] = struct{}{}
		}
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return func(rel string) bool {
		rel = filepath.ToSlash(rel)
		for d := range keep {
			if rel == d || strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
		return false
	}, nil
}

func (t Target) Pull(ctx context.Context, opts ports.BackupOpts) (*ports.BackupResult, error) {
	in := opts.Destination
	if in == "" {
		return nil, fmt.Errorf("tar restore needs --output <file.tar.zst> as the source")
	}
	f, err := os.Open(in)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(opts.ArchiveDir, filepath.Clean(hdr.Name))
		if !isWithin(opts.ArchiveDir, dst) {
			return nil, fmt.Errorf("tar entry escapes archive dir: %s", hdr.Name)
		}
		if opts.DryRun {
			count++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(w, tr); err != nil { //nolint:gosec // archive is user's own data
			w.Close()
			return nil, err
		}
		w.Close()
		count++
	}
	action := "import"
	note := ""
	if opts.DryRun {
		note = "dry run: nothing written"
	}
	return &ports.BackupResult{Target: "tar", Action: action, Changed: count, Note: note}, nil
}

func walkFiles(root string, fn func(path, rel string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return fn(path, rel)
	})
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !hasDotDotPrefix(rel)
}

func hasDotDotPrefix(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}
