package snapshot

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// TestExtensionAndFilename pins the user-visible filename suffix the
// CLI uses to suggest defaults. If we ever change a default, we want
// a noisy test failure to remind us to update the docs at the same
// time.
func TestExtensionAndFilename(t *testing.T) {
	cases := []struct {
		format, compress, wantExt string
	}{
		{"tar", "zstd", "tar.zst"},
		{"tar", "gzip", "tar.gz"},
		{"tar", "xz", "tar.xz"},
		{"tar", "none", "tar"},
		{"tar", "", "tar"},
		{"zip", "", "zip"},
		{"zip", "none", "zip"},
	}
	for _, c := range cases {
		if got := Extension(c.format, c.compress); got != c.wantExt {
			t.Errorf("Extension(%s,%s) = %q, want %q", c.format, c.compress, got, c.wantExt)
		}
	}

	stamp := time.Date(2026, 6, 13, 8, 50, 0, 0, time.UTC)
	name := DefaultFilename("tar", "zstd", stamp)
	if name != "tape-export-20260613-085000.tar.zst" {
		t.Errorf("default filename = %q", name)
	}
}

// TestValidateFormat: zip + non-none compression is a usage error
// because zip's compression is per-entry DEFLATE and silently
// dropping --compress would be a lie. Everything else valid passes.
func TestValidateFormat(t *testing.T) {
	good := [][2]string{
		{"", ""}, {"tar", ""}, {"tar", "zstd"}, {"tar", "gzip"},
		{"tar", "xz"}, {"tar", "none"},
		{"zip", ""}, {"zip", "none"},
	}
	for _, c := range good {
		if err := ValidateFormat(c[0], c[1]); err != nil {
			t.Errorf("ValidateFormat(%q,%q) errored: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{
		{"rar", ""}, {"tar", "snappy"}, {"zip", "gzip"}, {"zip", "zstd"},
	}
	for _, c := range bad {
		if err := ValidateFormat(c[0], c[1]); err == nil {
			t.Errorf("ValidateFormat(%q,%q) should have failed", c[0], c[1])
		}
	}
}

// seedArchive plants the minimum file layout the writer walks
// (<agent>/<project>/<sourceID>/session.json + meta.json). The
// stubArchive below pretends archive.List returns matching summaries
// when a filter is set — that's the contract the writer relies on.
func seedArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// stubArchive only implements the methods snapshot.Write actually
// calls (List). Everything else returns zero values so an accidental
// dependency on Get/Put would fail fast.
type stubArchive struct{ sums []model.Summary }

func (s stubArchive) Stale(ports.SessionRef) (bool, string, error)        { return false, "", nil }
func (s stubArchive) Put(context.Context, *model.Session, ports.SessionRef, string) error {
	return nil
}
func (s stubArchive) Get(context.Context, string) (*model.Session, error)           { return nil, nil }
func (s stubArchive) Count(context.Context, ports.Filter) (int, error)              { return 0, nil }
func (s stubArchive) Resolve(context.Context, string) (string, error)               { return "", nil }
func (s stubArchive) List(_ context.Context, _ ports.Filter) ([]model.Summary, error) {
	return s.sums, nil
}

// TestWriteFullExportTarZst: the default (tar + zstd) round-trips
// every file in the archive, skips .git, and reports a size > 0.
func TestWriteFullExportTarZst(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/s1/session.json": `{"text":"会话内容"}`,
		"codex/p/s1/raw/x.jsonl":  "raw",
		".git/config":             "must be skipped",
	})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	res, err := Write(context.Background(), nil, ports.ExportOpts{
		ArchiveDir: src, Output: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 || res.Format != "tar" || res.Bytes <= 0 {
		t.Errorf("result: %+v", res)
	}
	members := tarZstMembers(t, out)
	if _, ok := members["codex/p/s1/session.json"]; !ok {
		t.Errorf("session.json missing from artifact: %v", members)
	}
	if _, ok := members[".git/config"]; ok {
		t.Error(".git leaked into artifact")
	}
}

// TestWriteRedactionInStream verifies the local file is not touched
// when RedactCopy mutates the bytes on the way into the artifact.
// The redactor here is intentionally trivial — the real one is
// covered in the redact package.
func TestWriteRedactionInStream(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/s1/session.json": "key=AKIAIOSFODNN7EXAMPLE end",
	})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	_, err := Write(context.Background(), nil, ports.ExportOpts{
		ArchiveDir: src, Output: out,
		RedactCopy: func(_ string, data []byte) []byte {
			return bytes.ReplaceAll(data, []byte("AKIAIOSFODNN7EXAMPLE"), []byte("[GONE]"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	local, _ := os.ReadFile(filepath.Join(src, "codex/p/s1/session.json"))
	if !strings.Contains(string(local), "AKIA") {
		t.Error("local file was modified by export")
	}
	got := tarZstRead(t, out, "codex/p/s1/session.json")
	if strings.Contains(got, "AKIA") || !strings.Contains(got, "[GONE]") {
		t.Errorf("artifact not redacted: %q", got)
	}
}

// TestWriteFormatMatrix: every (format, compress) pair produces an
// artifact the matching standard-library reader can decode without
// errors. This protects against accidentally swapping writer order
// or forgetting to flush.
func TestWriteFormatMatrix(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/s1/session.json": "hello",
	})

	cases := []struct {
		format, compress, ext string
		open                  func(*testing.T, string) string
	}{
		{"tar", "zstd", "tar.zst", tarZstReadAll},
		{"tar", "gzip", "tar.gz", tarGzReadAll},
		{"tar", "xz", "tar.xz", tarXzReadAll},
		{"tar", "none", "tar", tarReadAll},
		{"zip", "", "zip", zipReadAll},
		{"zip", "none", "zip", zipReadAll},
	}
	for _, c := range cases {
		t.Run(c.format+"+"+c.compress, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out."+c.ext)
			res, err := Write(context.Background(), nil, ports.ExportOpts{
				ArchiveDir: src, Output: out,
				Format: c.format, Compress: c.compress,
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if res.Changed != 1 {
				t.Errorf("changed = %d, want 1", res.Changed)
			}
			if got := c.open(t, out); !strings.Contains(got, "hello") {
				t.Errorf("artifact body missing payload: %q", got)
			}
		})
	}
}

// TestWriteDryRunWritesNothing: dry-run reports the file count it
// would write but leaves the filesystem untouched.
func TestWriteDryRunWritesNothing(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/s1/session.json": "a",
		"codex/p/s1/raw/x.jsonl":  "b",
	})
	out := filepath.Join(t.TempDir(), "x.tar.zst")
	res, err := Write(context.Background(), nil, ports.ExportOpts{
		ArchiveDir: src, Output: out, DryRun: true,
	})
	if err != nil || res.Changed != 2 {
		t.Fatalf("dry run: %+v err=%v", res, err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("dry run wrote the artifact")
	}
}

// TestWriteFilterScopesByArchiveSummaries: with a Filter set, the
// writer asks the Archive for matching summaries and keeps only
// files under those session dirs. The non-matching session must not
// leak into the artifact.
func TestWriteFilterScopesByArchiveSummaries(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/sKeep/session.json":      "keep me",
		"codex/p/sKeep/raw/x.jsonl":       "keep raw",
		"claude-code/p/sDrop/session.json": "drop me",
	})
	out := filepath.Join(t.TempDir(), "a.tar.zst")

	arch := stubArchive{sums: []model.Summary{{
		ID: "codex/sKeep", Agent: "codex",
	}}}
	res, err := Write(context.Background(), arch, ports.ExportOpts{
		ArchiveDir: src, Output: out,
		Filter: ports.Filter{Agent: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 {
		t.Errorf("filtered changed = %d, want 2 (session.json + raw/x.jsonl)", res.Changed)
	}
	members := tarZstMembers(t, out)
	if _, ok := members["codex/p/sKeep/session.json"]; !ok {
		t.Error("kept session missing")
	}
	if _, ok := members["claude-code/p/sDrop/session.json"]; ok {
		t.Error("filtered-out session leaked into artifact")
	}
}

// TestWriteEmptyArchiveDirOK: a non-existent archive dir (fresh
// install, no sync yet) must not crash; export reports 0 files.
func TestWriteEmptyArchiveDirOK(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.tar.zst")
	res, err := Write(context.Background(), nil, ports.ExportOpts{
		ArchiveDir: filepath.Join(t.TempDir(), "nope"), Output: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 {
		t.Errorf("empty archive: changed = %d, want 0", res.Changed)
	}
}

// TestScanRunsRedactorOnSessionJSONOnly: only session.json files are
// fed to the scanner; raw/meta etc. are skipped to keep audit time
// bounded.
func TestScanRunsRedactorOnSessionJSONOnly(t *testing.T) {
	src := seedArchive(t, map[string]string{
		"codex/p/s1/session.json": "AKIAIOSFODNN7EXAMPLE",
		"codex/p/s1/meta.json":    "AKIAIOSFODNN7EXAMPLE",
		"codex/p/s1/raw/x.jsonl":  "AKIAIOSFODNN7EXAMPLE",
	})
	called := map[string]int{}
	findings, err := Scan(context.Background(), nil,
		ports.ExportOpts{ArchiveDir: src},
		func(p string, _ []byte) []Finding {
			called[p]++
			return []Finding{{Rule: "stub", Path: p, Line: 1}}
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Path != "codex/p/s1/session.json" {
		t.Errorf("findings = %+v (only session.json should be scanned)", findings)
	}
	if called["codex/p/s1/meta.json"] != 0 || called["codex/p/s1/raw/x.jsonl"] != 0 {
		t.Errorf("scanner called outside session.json: %v", called)
	}
}

// ---- format-specific readers ---------------------------------------

func tarZstMembers(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	return tarMembers(t, zr)
}

func tarMembers(t *testing.T, r io.Reader) map[string]string {
	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		buf, _ := io.ReadAll(tr)
		out[hdr.Name] = string(buf)
	}
}

func tarZstRead(t *testing.T, path, member string) string {
	for n, v := range tarZstMembers(t, path) {
		if n == member {
			return v
		}
	}
	t.Fatalf("member %s missing from %s", member, path)
	return ""
}

func tarZstReadAll(t *testing.T, path string) string {
	b := &strings.Builder{}
	for _, v := range tarZstMembers(t, path) {
		b.WriteString(v)
	}
	return b.String()
}

func tarGzReadAll(t *testing.T, path string) string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	b := &strings.Builder{}
	for _, v := range tarMembers(t, zr) {
		b.WriteString(v)
	}
	return b.String()
}

func tarXzReadAll(t *testing.T, path string) string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := xz.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b := &strings.Builder{}
	for _, v := range tarMembers(t, zr) {
		b.WriteString(v)
	}
	return b.String()
}

func tarReadAll(t *testing.T, path string) string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := &strings.Builder{}
	for _, v := range tarMembers(t, f) {
		b.WriteString(v)
	}
	return b.String()
}

func zipReadAll(t *testing.T, path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	b := &strings.Builder{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		b.Write(data)
	}
	return b.String()
}
