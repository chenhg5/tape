// Package tarball exports the archive as a single .tar.zst file. Because the
// tarball is a generated artifact (unlike the in-place git repo), contents
// can be redacted in-stream on the way in; local files stay untouched.
package tarball

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

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
	count := 0
	if opts.DryRun {
		err := walkFiles(opts.ArchiveDir, func(string, string) error { count++; return nil })
		if err != nil {
			return nil, err
		}
		return &ports.BackupResult{Target: "tar", Action: "export", Changed: count,
			Note: fmt.Sprintf("dry run: would write %d file(s) to %s", count, out)}, nil
	}

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
		_, err = tw.Write(data)
		count++
		return err
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
	return &ports.BackupResult{Target: "tar", Action: "export", Changed: count, Ref: out}, nil
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
