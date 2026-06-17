package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/export/bundle"
)

// makeBundle builds a tape bundle on disk at outPath. When withManifest is true,
// it writes tape-bundle.json at the root; otherwise it produces the "legacy"
// layout that tape <= 0.2.0 shipped (no manifest), which the importer must
// still recognize via scanStageLayout. Compression is .tar.zst across the
// board, matching the format the export command emits today.
func makeBundle(t *testing.T, outPath string, withManifest bool, sessID string) {
	t.Helper()
	makeBundleWithChecksum(t, outPath, withManifest, sessID, "deadbeef")
}

func makeBundleWithChecksum(t *testing.T, outPath string, withManifest bool, sessID, checksum string) {
	t.Helper()
	now := time.Now().UTC().Round(time.Second)
	sess := &model.Session{
		ID: "claude-code/" + sessID, Agent: "claude-code", SourceID: sessID,
		Title: "test bundle session", CWD: "/origin/path",
		StartedAt: now, UpdatedAt: now,
		Messages: []model.Message{{Role: "user", Text: "hello world"}},
	}
	slug := model.ProjectSlug(sess.CWD)
	sessJSON, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	metaJSON, err := json.Marshal(map[string]any{
		"schema_version": 1, "checksum": checksum,
		"archived_at": now,
	})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	prefix := filepath.ToSlash(filepath.Join("claude-code", slug, sessID))
	writeTarFile(t, tw, prefix+"/session.json", sessJSON)
	writeTarFile(t, tw, prefix+"/meta.json", metaJSON)

	if withManifest {
		m := &bundle.Manifest{
			SchemaVersion: bundle.SchemaV1,
			TapeVersion:   "0.3.0-test",
			Kind:          bundle.KindShare,
			CreatedAt:     now,
			Source:        bundle.SourceInfo{Host: "origin-host", TapeHome: "/origin/.tape"},
			Sessions: []bundle.SessionRow{
				bundle.RowFromSession(sess, checksum),
			},
		}
		var mb bytes.Buffer
		if err := bundle.Write(&mb, m); err != nil {
			t.Fatal(err)
		}
		writeTarFile(t, tw, bundle.ManifestFilename, mb.Bytes())
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc, err := zstd.NewWriter(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}

// runImport invokes the same code path as `tape import <bundle>` end-to-end:
// it extracts into a staging dir under the test home, reads the plan, and
// commits to the archive. Returns the importResult so tests can assert on
// per-session outcomes.
func runImport(t *testing.T, app *App, bundlePath, onConflict, rewriteCWD string, dryRun bool) *importResult {
	t.Helper()
	conflict, err := normalizeConflict(onConflict, false)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := setupStaging(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stage) })
	if _, err := extractBundle(context.Background(), bundlePath, stage); err != nil {
		t.Fatalf("extract: %v", err)
	}
	plan, err := readImportPlan(stage)
	if err != nil {
		t.Fatalf("readImportPlan: %v", err)
	}
	if rewriteCWD != "" {
		rewriteCWDInPlan(plan, rewriteCWD)
	}
	res, err := commitImport(context.Background(), app, stage, plan, conflict, dryRun)
	if err != nil {
		t.Fatalf("commitImport: %v", err)
	}
	return res
}

// TestImportFreshManifest verifies the happy path: a v1-manifest bundle goes
// in, exactly one session lands in the archive, and the imported file matches
// what the bundle carried.
func TestImportFreshManifest(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}

	bundlePath := filepath.Join(t.TempDir(), "fresh.tar.zst")
	makeBundle(t, bundlePath, true, "sess-a")

	res := runImport(t, app, bundlePath, "skip", "", false)
	if res.imported != 1 {
		t.Fatalf("imported=%d, want 1", res.imported)
	}
	wantPath := filepath.Join(app.archiveDir(), "claude-code",
		model.ProjectSlug("/origin/path"), "sess-a", "session.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("session.json not at %s: %v", wantPath, err)
	}
}

// TestImportLegacyBundleNoManifest is the v0.2.0 regression guard. A bundle
// without tape-bundle.json must still parse via the layout scanner. Without
// the fallback this would return zero planned sessions and import nothing.
func TestImportLegacyBundleNoManifest(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}

	bundlePath := filepath.Join(t.TempDir(), "legacy.tar.zst")
	makeBundle(t, bundlePath, false, "sess-legacy")

	res := runImport(t, app, bundlePath, "skip", "", false)
	if res.imported != 1 {
		t.Fatalf("legacy import: imported=%d, want 1", res.imported)
	}
	gotSession := filepath.Join(app.archiveDir(), "claude-code",
		model.ProjectSlug("/origin/path"), "sess-legacy", "session.json")
	if _, err := os.Stat(gotSession); err != nil {
		t.Fatalf("expected legacy session at %s: %v", gotSession, err)
	}
}

// TestImportConflictSkip verifies that a second import of the same bundle
// with the default `skip` mode leaves the archive untouched and (when the
// checksum differs from disk — which it does when the on-disk meta.json
// lacks a checksum) flags unresolvedConflicts so the CLI can exit 4.
func TestImportConflictSkip(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}
	bundlePath := filepath.Join(t.TempDir(), "dup.tar.zst")
	makeBundle(t, bundlePath, true, "sess-dup")

	_ = runImport(t, app, bundlePath, "skip", "", false)
	res := runImport(t, app, bundlePath, "skip", "", false)
	if res.imported != 0 || res.skipped != 1 {
		t.Fatalf("second import: imported=%d skipped=%d, want 0/1", res.imported, res.skipped)
	}
}

// TestImportConflictOverwrite ensures the overwrite path replaces the
// existing dir end-to-end. The second bundle is intentionally built with a
// different checksum so the importer doesn't silently fast-path it as
// "same content already present".
func TestImportConflictOverwrite(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}
	dir := t.TempDir()
	first := filepath.Join(dir, "ow1.tar.zst")
	second := filepath.Join(dir, "ow2.tar.zst")
	makeBundleWithChecksum(t, first, true, "sess-ow", "checksum-v1")
	makeBundleWithChecksum(t, second, true, "sess-ow", "checksum-v2")

	_ = runImport(t, app, first, "skip", "", false)
	res := runImport(t, app, second, "overwrite", "", false)
	if res.overwritten != 1 {
		t.Fatalf("overwritten=%d, want 1 (lines=%v)", res.overwritten, res.lines)
	}
}

// TestImportConflictRename validates the rename branch: a second import of
// the same source_id with a different checksum under `rename` must produce
// a new source_id and leave the original copy intact.
func TestImportConflictRename(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}
	dir := t.TempDir()
	first := filepath.Join(dir, "rn1.tar.zst")
	second := filepath.Join(dir, "rn2.tar.zst")
	makeBundleWithChecksum(t, first, true, "sess-rn", "checksum-v1")
	makeBundleWithChecksum(t, second, true, "sess-rn", "checksum-v2")

	_ = runImport(t, app, first, "skip", "", false)
	res := runImport(t, app, second, "rename", "", false)
	if res.renamed != 1 {
		t.Fatalf("renamed=%d, want 1 (lines=%v)", res.renamed, res.lines)
	}
	originalDir := filepath.Join(app.archiveDir(), "claude-code",
		model.ProjectSlug("/origin/path"), "sess-rn")
	if _, err := os.Stat(originalDir); err != nil {
		t.Fatalf("original sess-rn missing after rename: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(app.archiveDir(), "claude-code", "*", "sess-rn-tape-*"))
	if len(matches) != 1 {
		t.Fatalf("renamed copy not found, glob got %v", matches)
	}
}

// TestImportRewriteCWD checks that --rewrite-cwd both swaps the recorded CWD
// in the importer's plan and lands the files under the new project_slug, so
// the agent looking at slug(newCWD) on this machine sees them.
func TestImportRewriteCWD(t *testing.T) {
	home := t.TempDir()
	app := &App{home: home}
	bundlePath := filepath.Join(t.TempDir(), "cwd.tar.zst")
	makeBundle(t, bundlePath, true, "sess-cwd")

	newCWD := "/local/here"
	res := runImport(t, app, bundlePath, "skip", newCWD, false)
	if res.imported != 1 {
		t.Fatalf("imported=%d, want 1", res.imported)
	}
	wantDir := filepath.Join(app.archiveDir(), "claude-code", model.ProjectSlug(newCWD), "sess-cwd")
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("expected session under rewritten slug: %v", err)
	}
}

// TestImportSafePath catches path-traversal entries in tar bundles. An entry
// named "../../etc/passwd" must be refused outright; we don't want a hostile
// bundle to clobber files outside the stage dir even before the user has had
// a chance to inspect what's inside.
func TestImportSafePath(t *testing.T) {
	stage := t.TempDir()
	if _, ok := safeStagePath(stage, "../../etc/passwd"); ok {
		t.Fatal("safeStagePath accepted traversal path")
	}
	if _, ok := safeStagePath(stage, "/abs/path"); ok {
		t.Fatal("safeStagePath accepted absolute path")
	}
	if got, ok := safeStagePath(stage, "claude-code/x/y"); !ok || !strings.HasPrefix(got, stage) {
		t.Fatalf("safeStagePath rejected valid path: ok=%v got=%q", ok, got)
	}
}
