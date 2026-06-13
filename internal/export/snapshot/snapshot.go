// Package snapshot writes the archive (or a filter-scoped subset of it)
// as a single artifact on disk. It supports two container formats and
// four compression codecs so callers can pick the right tradeoff:
//
//	tar + zstd  → .tar.zst  — best ratio + speed (default)
//	tar + gzip  → .tar.gz   — most-portable compressed format
//	tar + xz    → .tar.xz   — best ratio (slow), Linux-flavored
//	tar + none  → .tar      — already-compressed source, or piping
//	zip + none  → .zip      — Windows-friendly, per-entry DEFLATE
//
// The package is intentionally write-only: importing a snapshot back is
// a `tar xf` / `unzip` away and not a tape responsibility (the user said
// so explicitly — keep this small). The Filter passed in via ExportOpts
// is the same one ls/search use, so `tape export --agent codex --since
// 7d` lines up with the user's mental model of "what's in there".
package snapshot

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// FormatTar / FormatZip are the only two container formats. We keep
// them as string constants instead of an enum so callers (and tests)
// can compare to flag values without wrapper types.
const (
	FormatTar = "tar"
	FormatZip = "zip"

	CompressZstd = "zstd"
	CompressGzip = "gzip"
	CompressXz   = "xz"
	CompressNone = "none"
)

// DefaultFilename returns the suggested output file basename for the
// given format/compress pair, based on the current time. Stable enough
// to be predictable (timestamp resolution: second), unique enough that
// back-to-back exports don't overwrite each other.
func DefaultFilename(format, compress string, now time.Time) string {
	stamp := now.UTC().Format("20060102-150405")
	return "tape-export-" + stamp + "." + Extension(format, compress)
}

// Extension returns the conventional extension for a (format,
// compress) pair, *without* the leading dot. `tar.zst` for tar+zstd,
// `zip` for zip (zip's own DEFLATE is always implied), etc.
func Extension(format, compress string) string {
	if format == FormatZip {
		return "zip"
	}
	switch compress {
	case CompressZstd:
		return "tar.zst"
	case CompressGzip:
		return "tar.gz"
	case CompressXz:
		return "tar.xz"
	default:
		return "tar"
	}
}

// ValidateFormat normalizes & checks the (format, compress) pair.
// Unknown values become usage errors so users see "invalid --format
// rar" instead of a cryptic compression error two stack frames later.
// zip + non-none compress is also rejected because zip's compression
// is per-entry DEFLATE and ignoring --compress would silently lie.
func ValidateFormat(format, compress string) error {
	switch format {
	case "", FormatTar, FormatZip:
	default:
		return fmt.Errorf("--format %q: must be tar or zip", format)
	}
	switch compress {
	case "", CompressZstd, CompressGzip, CompressXz, CompressNone:
	default:
		return fmt.Errorf("--compress %q: must be zstd, gzip, xz or none", compress)
	}
	if format == FormatZip && compress != "" && compress != CompressNone {
		return fmt.Errorf("zip uses its own DEFLATE; --compress %s is not applicable", compress)
	}
	return nil
}

// Write executes one export. Output must already be set; if Format /
// Compress are blank they default to tar + zstd. The function streams
// (no full in-memory buffering), so very large archives are fine.
//
// A nil Filter exports everything. Otherwise we resolve the filter
// through ports.Archive at call sites (see cli/export.go) and pass
// the resulting session ID set in via opts.Filter — that keeps this
// package free of any archive-layout coupling.
func Write(ctx context.Context, archive ports.Archive, opts ports.ExportOpts) (*ports.ExportResult, error) {
	if opts.Output == "" && !opts.DryRun {
		return nil, fmt.Errorf("snapshot.Write needs Output (or DryRun)")
	}
	format := opts.Format
	if format == "" {
		format = FormatTar
	}
	compress := opts.Compress
	if compress == "" && format == FormatTar {
		compress = CompressZstd
	}
	if err := ValidateFormat(format, compress); err != nil {
		return nil, err
	}

	keep, err := buildKeep(ctx, archive, opts.Filter)
	if err != nil {
		return nil, err
	}

	// Pre-scan for a stable progress denominator and to make dry-run
	// cheap (we walk once instead of opening a writer for nothing).
	var total int64
	if err := walkFiles(opts.ArchiveDir, func(_, rel string) error {
		if keep(rel) {
			total++
		}
		return nil
	}); err != nil {
		return nil, err
	}

	res := &ports.ExportResult{Format: format, Changed: int(total)}
	if opts.DryRun {
		res.Note = fmt.Sprintf("dry run: would write %d file(s)", total)
		if opts.Output != "" {
			res.Output = opts.Output
			res.Note += " to " + opts.Output
		}
		return res, nil
	}

	out, err := os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	res.Output = opts.Output

	count, writeErr := writeArchive(ctx, out, format, compress, opts, keep, total)
	res.Changed = count
	if writeErr != nil {
		// best-effort cleanup so half-written artifacts don't linger
		os.Remove(opts.Output)
		return nil, writeErr
	}
	if stat, err := os.Stat(opts.Output); err == nil {
		res.Bytes = stat.Size()
	}
	return res, nil
}

// writeArchive picks the right writer stack for the chosen
// format+compress combo and pipes every kept file through it. The
// tar/zip split is here (not in Write) so we don't open the output
// file before we know the combo is valid.
func writeArchive(ctx context.Context, out io.Writer, format, compress string,
	opts ports.ExportOpts, keep func(string) bool, total int64) (int, error) {

	if format == FormatZip {
		zw := zip.NewWriter(out)
		count, err := pipeFiles(opts, keep, total, func(rel string, data []byte, info os.FileInfo) error {
			fh, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			fh.Name = rel
			fh.Method = zip.Deflate
			w, err := zw.CreateHeader(fh)
			if err != nil {
				return err
			}
			_, err = w.Write(data)
			return err
		})
		if err != nil {
			zw.Close()
			return count, err
		}
		return count, zw.Close()
	}

	// tar family: wrap the file in a compressor (or not) and then in a
	// tar writer. Order matters — close tar → close compressor → close
	// file, otherwise the compressor never flushes its trailer.
	var compressed io.WriteCloser
	switch compress {
	case CompressZstd, "":
		zw, err := zstd.NewWriter(out)
		if err != nil {
			return 0, err
		}
		compressed = zw
	case CompressGzip:
		compressed = gzip.NewWriter(out)
	case CompressXz:
		xw, err := xz.NewWriter(out)
		if err != nil {
			return 0, err
		}
		compressed = xw
	case CompressNone:
		// Wrap as a no-op closer so the cleanup path below is uniform.
		compressed = nopCloser{out}
	}
	tw := tar.NewWriter(compressed)
	count, err := pipeFiles(opts, keep, total, func(rel string, data []byte, info os.FileInfo) error {
		hdr := &tar.Header{
			Name:    rel,
			Mode:    0o600,
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	})
	if err != nil {
		tw.Close()
		compressed.Close()
		return count, err
	}
	if err := tw.Close(); err != nil {
		return count, err
	}
	return count, compressed.Close()
}

// pipeFiles is the common archive-agnostic loop: walk the archive,
// keep what passes the filter, redact when asked, hand the bytes to
// the format-specific add function. Returns the number of files
// actually written.
func pipeFiles(opts ports.ExportOpts, keep func(string) bool, total int64,
	add func(rel string, data []byte, info os.FileInfo) error) (int, error) {
	count := 0
	err := walkFiles(opts.ArchiveDir, func(path, rel string) error {
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
		if err := add(rel, data, info); err != nil {
			return err
		}
		count++
		if opts.OnProgress != nil {
			opts.OnProgress(int64(count), total, rel)
		}
		return nil
	})
	return count, err
}

// buildKeep turns a Filter into a predicate over archive-relative
// paths. The empty filter accepts everything — that's the common case
// (full export) and we don't want to pay an archive.List for it.
//
// With any filter set we ask the archive layer to list the matching
// summaries, then keep every file under those session directories.
// Doing the resolution here (not at the CLI layer) means the snapshot
// package owns the "kept set" abstraction end-to-end and tests don't
// have to thread an Archive into every Filter case.
func buildKeep(ctx context.Context, archive ports.Archive, f ports.Filter) (func(string) bool, error) {
	if filterIsEmpty(f) {
		return func(string) bool { return true }, nil
	}
	if archive == nil {
		return nil, fmt.Errorf("filter set but no archive bound; cannot resolve sessions")
	}
	sums, err := archive.List(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return func(string) bool { return false }, nil
	}
	// A summary ID is "<agent>/<source-id>"; the archive lays sessions
	// out as "<agent>/<project-slug>/<source-id>/...". We can't recover
	// the project slug from the summary alone (it's a function of CWD),
	// so we glob for any directory ending in /<source-id>.
	keepDir := map[string]struct{}{}
	for _, s := range sums {
		agent, sourceID, ok := strings.Cut(s.ID, "/")
		if !ok {
			continue
		}
		keepDir[agent+"/"+sourceID] = struct{}{}
	}
	return func(rel string) bool {
		rel = filepath.ToSlash(rel)
		for k := range keepDir {
			agent, sid, _ := strings.Cut(k, "/")
			// Match any path shaped <agent>/.../<sid> or descendant.
			if !strings.HasPrefix(rel, agent+"/") {
				continue
			}
			if strings.HasSuffix(rel, "/"+sid+"/session.json") ||
				strings.HasSuffix(rel, "/"+sid+"/meta.json") ||
				strings.Contains(rel, "/"+sid+"/") {
				return true
			}
		}
		return false
	}, nil
}

func filterIsEmpty(f ports.Filter) bool {
	return f.Agent == "" && f.Project == "" && f.Host == "" && f.Since.IsZero()
}

// walkFiles is the canonical "iterate every file under root" loop,
// shared by buildKeep's pre-scan and the writer. .git is skipped
// because we used to be a git repo backend and old archives might
// still carry one; never include it in the artifact.
func walkFiles(root string, fn func(path, rel string) error) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil // empty archive dir is not an error
			}
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
		return fn(path, filepath.ToSlash(rel))
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Scan walks the archive (honoring the same filter) and runs every
// session.json through the redactor's secret detector. Returns the
// matched findings without writing anything; `tape export
// --scan-only` calls this and prints the result.
//
// Lives here (and not next to redact) because it shares the same
// keep-set + walk machinery as the writer, so a future "snapshot
// excludes session X" rule applies to both paths automatically.
func Scan(ctx context.Context, archive ports.Archive, opts ports.ExportOpts,
	scanFn func(path string, data []byte) []Finding) ([]Finding, error) {
	keep, err := buildKeep(ctx, archive, opts.Filter)
	if err != nil {
		return nil, err
	}
	var out []Finding
	err = walkFiles(opts.ArchiveDir, func(path, rel string) error {
		if !keep(rel) || filepath.Base(rel) != "session.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, scanFn(rel, data)...)
		return nil
	})
	return out, err
}

// Finding mirrors redact.Finding's shape but lives here so the
// snapshot package doesn't import the redactor (which in turn would
// drag the redactor's deps into anyone who only wants exports).
// Callers convert at the boundary.
type Finding struct {
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Preview string `json:"preview,omitempty"`
}

// nopCloser adapts an io.Writer (the raw file when --compress=none)
// to the io.WriteCloser shape the tar pipeline expects.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// Compile-time sanity check that model.Session still exists — we
// don't use it directly, but its presence in ports.Archive (via
// Get) is what makes the snapshot reproducible.
var _ = (*model.Session)(nil)
