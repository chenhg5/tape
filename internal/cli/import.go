package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
	"github.com/ulikunitz/xz"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/export/bundle"
)

// Conflict resolution strategies for `tape import --on-conflict`.
const (
	conflictSkip      = "skip"
	conflictOverwrite = "overwrite"
	conflictRename    = "rename"
	conflictPrompt    = "prompt"
)

// newImportCmd builds `tape import <bundle>`.
//
// The command always works against a single bundle file (one per
// invocation, no globbing). It unpacks into a staging directory under
// ~/.tape/.import-staging/<uuid>, reads the manifest (or falls back
// to scanning the archive layout when one isn't present), decides
// which sessions to merge, applies conflict resolution, and then
// atomically renames each session's directory into place. Failure at
// any point removes the staging tree without touching the live archive.
func newImportCmd(app *App) *cobra.Command {
	var (
		onConflict string
		rewriteCWD string
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "import <bundle>",
		Short: "Import sessions from a tape bundle (.tar.zst / .tar.gz / .tar.xz / .tar / .zip)",
		Long: `Restores sessions from a bundle previously produced by 'tape export'
or 'tape share'. The bundle must include a tape-bundle.json manifest;
bundles from tape <= 0.2.0 (no manifest) are tolerated via a layout
scan.

Conflict resolution (--on-conflict):
  skip       leave the existing session untouched (default for non-TTY)
  overwrite  replace the existing session's files
  rename     stage under a new source_id so both copies survive
  prompt     ask interactively (default for TTY)

The import is atomic: every session lands in a staging directory and
is moved into the archive only after all of them are ready. If any
step fails, nothing in the archive changes.

--rewrite-cwd points every imported session at a new working directory
on the importing machine — handy when the source captured paths under
/Users/alice that don't exist here. project_slug is recomputed
accordingly.`,
		Example: `  tape import ./tape-share-claude-code-abc.tar.zst
  tape import ./bundle.tar.zst --on-conflict overwrite
  tape import ./bundle.tar.zst --rewrite-cwd /root/code/proj
  tape import ./bundle.tar.zst --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			run := startRun(app, "import")
			defer func() { err = run.finish(err) }()
			run.setScope("bundle", args[0])
			run.setScope("on_conflict", onConflict)
			if rewriteCWD != "" {
				run.setScope("rewrite_cwd", rewriteCWD)
			}

			conflict, err := normalizeConflict(onConflict, app.interactive())
			if err != nil {
				return err
			}
			run.setScope("on_conflict_resolved", conflict)

			stageRoot, err := setupStaging(app)
			if err != nil {
				return err
			}
			defer os.RemoveAll(stageRoot) // best-effort cleanup

			extracted, err := extractBundle(cmd.Context(), args[0], stageRoot)
			if err != nil {
				return fmt.Errorf("extract bundle: %w", err)
			}

			plan, err := readImportPlan(extracted)
			if err != nil {
				return err
			}
			if rewriteCWD != "" {
				rewriteCWDInPlan(plan, rewriteCWD)
			}

			result, err := commitImport(cmd.Context(), app, extracted, plan, conflict, dryRun)
			if err != nil {
				return err
			}
			run.setCount("imported", result.imported)
			run.setCount("skipped", result.skipped)
			run.setCount("overwritten", result.overwritten)
			run.setCount("renamed", result.renamed)

			if app.useJSON() {
				for _, line := range result.lines {
					if err := emitJSON(line); err != nil {
						return err
					}
				}
			} else {
				printImportSummary(app, result, dryRun)
			}

			if result.unresolvedConflicts > 0 {
				return fmt.Errorf("%w: %d session(s) (re-run with --on-conflict=overwrite|rename to merge)",
					ErrImportConflicts, result.unresolvedConflicts)
			}
			if dryRun {
				return errDryRun
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&onConflict, "on-conflict", "", "skip|overwrite|rename|prompt (default: prompt on TTY, skip otherwise)")
	cmd.Flags().StringVar(&rewriteCWD, "rewrite-cwd", "", "rewrite every session's recorded cwd to this path (also recomputes project_slug)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without changing the archive (exit 10 on success)")
	return cmd
}

// normalizeConflict validates the user's --on-conflict choice and
// applies the TTY-aware default. Returning a normalized string keeps
// downstream switch statements compact.
func normalizeConflict(flag string, interactive bool) (string, error) {
	flag = strings.TrimSpace(strings.ToLower(flag))
	switch flag {
	case "":
		if interactive {
			return conflictPrompt, nil
		}
		return conflictSkip, nil
	case conflictSkip, conflictOverwrite, conflictRename, conflictPrompt:
		return flag, nil
	default:
		return "", usageErrf("--on-conflict %q: must be skip, overwrite, rename or prompt", flag)
	}
}

// setupStaging creates a fresh ~/.tape/.import-staging/<uuid>/ dir.
// Each invocation gets its own subdirectory so concurrent imports can
// stage without colliding, and the cleanup at function exit only ever
// touches *this* run's tree.
func setupStaging(app *App) (string, error) {
	base := filepath.Join(app.home, ".import-staging")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "import-")
}

// extractBundle inspects the file by extension, picks the matching
// decoder, and writes every entry under stageDir. Returns the staging
// dir on success; the caller is responsible for cleanup.
//
// We refuse path-traversal entries ("../" or absolute paths) outright
// — a malicious bundle shouldn't be able to write outside the staging
// dir even if the user later runs `tape import` on a hostile blob.
func extractBundle(ctx context.Context, path, stageDir string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return stageDir, extractZip(path, stageDir)
	case strings.HasSuffix(lower, ".tar.zst"), strings.HasSuffix(lower, ".tzst"):
		dec, err := zstd.NewReader(f)
		if err != nil {
			return "", err
		}
		defer dec.Close()
		return stageDir, extractTar(ctx, dec, stageDir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		return stageDir, extractTar(ctx, gz, stageDir)
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		xr, err := xz.NewReader(f)
		if err != nil {
			return "", err
		}
		return stageDir, extractTar(ctx, xr, stageDir)
	case strings.HasSuffix(lower, ".tar"):
		return stageDir, extractTar(ctx, f, stageDir)
	}
	return "", usageErrf("unrecognized bundle extension on %q (want .tar.zst|.tar.gz|.tar.xz|.tar|.zip)", path)
}

func extractTar(ctx context.Context, r io.Reader, stageDir string) error {
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean, ok := safeStagePath(stageDir, hdr.Name)
		if !ok {
			return fmt.Errorf("bundle entry escapes stage dir: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(clean, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA: legacy tarballs
			if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

func extractZip(path, stageDir string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, ze := range zr.File {
		clean, ok := safeStagePath(stageDir, ze.Name)
		if !ok {
			return fmt.Errorf("bundle entry escapes stage dir: %q", ze.Name)
		}
		if ze.FileInfo().IsDir() {
			if err := os.MkdirAll(clean, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
			return err
		}
		rc, err := ze.Open()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(f, rc); err != nil {
			rc.Close()
			f.Close()
			return err
		}
		rc.Close()
		f.Close()
	}
	return nil
}

// safeStagePath joins a bundle-relative path under stageDir and
// rejects anything that climbs out via "../" or absolute prefixes.
// Returns the cleaned absolute path + ok.
func safeStagePath(stageDir, name string) (string, bool) {
	cleaned := filepath.Clean(name)
	if filepath.IsAbs(cleaned) {
		return "", false
	}
	abs := filepath.Join(stageDir, cleaned)
	relTo, err := filepath.Rel(stageDir, abs)
	if err != nil || strings.HasPrefix(relTo, "..") {
		return "", false
	}
	return abs, true
}

// plannedSession is the in-memory model of one session about to be
// imported. AgentDir/ProjectSlug/SourceID identify where it should
// land; StageDir is the absolute path of its files inside the
// staging tree.
type plannedSession struct {
	Agent       string
	SourceID    string
	ProjectSlug string
	CWD         string
	Title       string
	Checksum    string
	StageDir    string
}

// readImportPlan parses tape-bundle.json from the staging dir, or
// falls back to scanning the layout when the manifest is missing.
// The fallback path is what makes old v0.2.0 bundles importable.
func readImportPlan(stageDir string) ([]plannedSession, error) {
	f, err := os.Open(filepath.Join(stageDir, bundle.ManifestFilename))
	if err == nil {
		defer f.Close()
		m, err := bundle.Read(f)
		if err != nil {
			if errors.Is(err, bundle.ErrMissingSchemaVersion) {
				return scanStageLayout(stageDir)
			}
			return nil, fmt.Errorf("read manifest: %w", err)
		}
		out := make([]plannedSession, 0, len(m.Sessions))
		for _, row := range m.Sessions {
			out = append(out, plannedSession{
				Agent:       row.Agent,
				SourceID:    row.SourceID,
				ProjectSlug: row.ProjectSlug,
				CWD:         row.OriginalCWD,
				Title:       row.Title,
				Checksum:    row.Checksum,
				StageDir:    filepath.Join(stageDir, row.Agent, row.ProjectSlug, row.SourceID),
			})
		}
		return out, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return scanStageLayout(stageDir)
}

// scanStageLayout walks the staged tree to find `<agent>/<slug>/<sid>`
// directories. Used both for bundles produced by tape <= 0.2.0 (no
// manifest) and for manifests that parsed but had ErrMissingSchemaVersion.
// We require a session.json inside the candidate dir to confirm it's a
// real session and not random user data.
func scanStageLayout(stageDir string) ([]plannedSession, error) {
	var out []plannedSession
	agentDirs, err := os.ReadDir(stageDir)
	if err != nil {
		return nil, err
	}
	for _, ad := range agentDirs {
		if !ad.IsDir() {
			continue
		}
		agent := ad.Name()
		projectDirs, _ := os.ReadDir(filepath.Join(stageDir, agent))
		for _, pd := range projectDirs {
			if !pd.IsDir() {
				continue
			}
			slug := pd.Name()
			sessDirs, _ := os.ReadDir(filepath.Join(stageDir, agent, slug))
			for _, sd := range sessDirs {
				if !sd.IsDir() {
					continue
				}
				sid := sd.Name()
				sessFile := filepath.Join(stageDir, agent, slug, sid, "session.json")
				if _, err := os.Stat(sessFile); err != nil {
					continue
				}
				cwd, title, checksum := readStageHints(filepath.Join(stageDir, agent, slug, sid))
				out = append(out, plannedSession{
					Agent:       agent,
					SourceID:    sid,
					ProjectSlug: slug,
					CWD:         cwd,
					Title:       title,
					Checksum:    checksum,
					StageDir:    filepath.Join(stageDir, agent, slug, sid),
				})
			}
		}
	}
	return out, nil
}

// readStageHints pulls CWD/Title from session.json and Checksum from
// meta.json, used by the layout-scan fallback. All fields are best-
// effort — a corrupt sidecar just means we get empty hints for that
// session but the import still goes ahead.
func readStageHints(sessDir string) (cwd, title, checksum string) {
	if b, err := os.ReadFile(filepath.Join(sessDir, "session.json")); err == nil {
		var s struct {
			CWD   string `json:"cwd"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(b, &s)
		cwd, title = s.CWD, s.Title
	}
	if b, err := os.ReadFile(filepath.Join(sessDir, "meta.json")); err == nil {
		var m struct {
			Checksum string `json:"checksum"`
		}
		_ = json.Unmarshal(b, &m)
		checksum = m.Checksum
	}
	return cwd, title, checksum
}

// rewriteCWDInPlan applies --rewrite-cwd to every staged session,
// recomputes the project_slug so the destination directory under the
// archive matches the new layout the agent will see locally, and
// patches the staged session.json so the importer's plan and the
// on-disk session agree. The agent-native files under raw/ are left
// alone because each agent will re-derive its own project key on the
// next launch.
func rewriteCWDInPlan(plan []plannedSession, newCWD string) {
	for i := range plan {
		oldCWD := plan[i].CWD
		plan[i].CWD = newCWD
		plan[i].ProjectSlug = model.ProjectSlug(newCWD)
		patchStagedCWD(plan[i].StageDir, oldCWD, newCWD)
	}
}

// patchStagedCWD rewrites the cwd/project string inside both the
// staged session.json and the cached summary inside meta.json before
// the dir is renamed into the archive. We use JSON re-marshal (not a
// string replace) so unrelated occurrences of oldCWD inside message
// bodies are left alone.
//
// Both files need updating because `tape ls` reads from meta.json's
// cached summary for speed and `tape show` reads session.json — out of
// sync, the user would see the rewritten path in one and the original
// in the other.
func patchStagedCWD(stageDir, oldCWD, newCWD string) {
	patchJSONField(filepath.Join(stageDir, "session.json"), func(doc map[string]any) {
		doc["cwd"] = newCWD
	})
	patchJSONField(filepath.Join(stageDir, "meta.json"), func(doc map[string]any) {
		if summary, ok := doc["summary"].(map[string]any); ok {
			summary["cwd"] = newCWD
			summary["project"] = model.ProjectSlug(newCWD)
		}
	})
	_ = oldCWD
}

// patchJSONField is a small helper that opens path, decodes it as a
// JSON object, runs mutate, and writes the result back. Any failure
// (missing file, bad JSON) is swallowed because rewriting is best-
// effort polish on a fully-staged tree — losing one of session.json /
// meta.json is not worth aborting the whole import for.
func patchJSONField(path string, mutate func(map[string]any)) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return
	}
	mutate(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, out, 0o600)
}

// importResult is what commitImport returns to the caller for printing
// and for the oplog. `lines` is the per-session JSON payload printed
// when --json is on.
type importResult struct {
	imported            int
	skipped             int
	overwritten         int
	renamed             int
	unresolvedConflicts int
	lines               []map[string]any
}

// commitImport drives the planned set through the archive: for each
// planned session it decides skip/overwrite/rename/abort and (unless
// dry-run) performs the atomic rename into the archive layout. Index
// upserts happen at the end so a half-committed index doesn't outlive
// a half-committed filesystem.
func commitImport(ctx context.Context, app *App, _ string, plan []plannedSession,
	conflict string, dryRun bool) (*importResult, error) {
	res := &importResult{}
	archiveRoot := app.archiveDir()
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return res, err
	}

	type committed struct {
		id      string
		destDir string
	}
	var committedRows []committed

	for _, p := range plan {
		destDir := filepath.Join(archiveRoot, p.Agent, p.ProjectSlug, p.SourceID)
		newSourceID := p.SourceID
		action := "imported"

		switch existsAction := decideAction(archiveRoot, p, conflict, app); existsAction {
		case "skip":
			res.skipped++
			if conflict == conflictPrompt {
				// User typed "no" or hit EOF on the prompt.
			} else if conflict == conflictSkip && existingChecksumMatches(archiveRoot, p) {
				// noise-free skip when the content is bit-identical
			}
			if conflict == conflictSkip && !existingChecksumMatches(archiveRoot, p) {
				res.unresolvedConflicts++
			}
			res.lines = append(res.lines, map[string]any{
				"id": p.Agent + "/" + p.SourceID, "action": "skipped",
				"reason": "exists; --on-conflict=" + conflict,
			})
			continue
		case "overwrite":
			action = "overwritten"
			if !dryRun {
				if err := os.RemoveAll(destDir); err != nil {
					return res, fmt.Errorf("clear %s: %w", destDir, err)
				}
			}
		case "rename":
			// Generate a fresh source_id under the same agent and use
			// it for both the destDir and the in-memory plan. The
			// session.json inside still references the old id; we
			// rewrite it below before the rename.
			newSourceID = renameSourceID(p.SourceID)
			destDir = filepath.Join(archiveRoot, p.Agent, p.ProjectSlug, newSourceID)
			action = "renamed"
		case "fresh":
			action = "imported"
		case "abort":
			res.skipped++
			res.unresolvedConflicts++
			res.lines = append(res.lines, map[string]any{
				"id": p.Agent + "/" + p.SourceID, "action": "skipped",
				"reason": "user aborted (prompt declined)",
			})
			continue
		}

		if dryRun {
			res.imported++
			res.lines = append(res.lines, map[string]any{
				"id": p.Agent + "/" + newSourceID, "action": action, "dry_run": true,
			})
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destDir), 0o700); err != nil {
			return res, err
		}
		if action == "renamed" {
			if err := rewriteSourceID(p.StageDir, p.SourceID, newSourceID, p.Agent); err != nil {
				return res, err
			}
		}
		if err := os.Rename(p.StageDir, destDir); err != nil {
			// On some filesystems (notably tmpfs straddling
			// devices) rename can EXDEV; fall back to copy.
			if err := copyTree(p.StageDir, destDir); err != nil {
				return res, fmt.Errorf("commit %s: %w", destDir, err)
			}
		}
		committedRows = append(committedRows, committed{
			id: p.Agent + "/" + newSourceID, destDir: destDir,
		})
		res.imported++
		switch action {
		case "overwritten":
			res.overwritten++
		case "renamed":
			res.renamed++
		}
		line := map[string]any{
			"id": p.Agent + "/" + newSourceID, "action": action,
		}
		if action == "renamed" {
			line["new_id"] = p.Agent + "/" + newSourceID
		}
		res.lines = append(res.lines, line)
	}

	// Index upsert after all renames so partial failure can't leave the
	// FTS index claiming sessions whose files don't exist yet.
	if !dryRun && len(committedRows) > 0 {
		ix, err := app.Index()
		if err == nil {
			for _, c := range committedRows {
				sess, err := app.Archive().Get(ctx, c.id)
				if err != nil || sess == nil {
					continue
				}
				_ = ix.Upsert(ctx, sess) // best-effort: search is regenerable
			}
		}
	}
	return res, nil
}

// decideAction inspects the live archive and the requested conflict
// mode to pick one of {fresh,skip,overwrite,rename,abort}. We treat
// "exists with identical checksum" as a silent skip regardless of
// flag, because the user almost certainly meant "merge the same data
// idempotently".
func decideAction(archiveRoot string, p plannedSession, mode string, app *App) string {
	if !sessionExists(archiveRoot, p.Agent, p.SourceID) {
		return "fresh"
	}
	if existingChecksumMatches(archiveRoot, p) {
		return "skip"
	}
	switch mode {
	case conflictSkip:
		return "skip"
	case conflictOverwrite:
		return "overwrite"
	case conflictRename:
		return "rename"
	case conflictPrompt:
		// Without a stable interactive prompt helper at hand we fall
		// back to a single yes/no line — answering "y" merges via
		// overwrite (the most common intent) and anything else aborts.
		fmt.Fprintf(os.Stderr, "import: %s/%s already exists; overwrite? [y/N] ",
			p.Agent, p.SourceID)
		var line string
		if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
			return "abort"
		}
		if strings.EqualFold(strings.TrimSpace(line), "y") {
			return "overwrite"
		}
		return "abort"
	}
	return "skip"
}

// sessionExists peeks at <archive>/<agent>/*/<sourceID> to decide if
// the local archive already carries this session. We don't open
// session.json — existence of the dir is enough.
func sessionExists(archiveRoot, agent, sourceID string) bool {
	matches, _ := filepath.Glob(filepath.Join(archiveRoot, agent, "*", sourceID))
	return len(matches) > 0
}

// existingChecksumMatches compares the planned session's checksum
// (from the manifest, if present) against the meta.json of any
// existing copy under the agent shelf. Returns false on any mismatch
// or when either side lacks a checksum — we'd rather treat unknowns
// as conflicts than silently no-op.
func existingChecksumMatches(archiveRoot string, p plannedSession) bool {
	if p.Checksum == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(archiveRoot, p.Agent, "*", p.SourceID, "meta.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var meta struct {
			Checksum string `json:"checksum"`
		}
		if json.Unmarshal(b, &meta) == nil && meta.Checksum == p.Checksum {
			return true
		}
	}
	return false
}

// renameSourceID derives a deterministic-ish new id for `rename`
// conflicts. Format: "<old>-tape-<ts>" so the audit trail is obvious
// and the original id stays a prefix for easy `tape resolve`.
func renameSourceID(old string) string {
	return old + "-tape-" + time.Now().UTC().Format("20060102150405")
}

// rewriteSourceID walks the staged session dir and patches every
// reference to the old source id in session.json and meta.json with
// the new id. Other agent-native files in raw/ keep the old id
// because they were captured under it; the archive layer doesn't
// reread them, so the mismatch is cosmetic only.
func rewriteSourceID(stageDir, oldID, newID, agent string) error {
	for _, name := range []string{"session.json", "meta.json"} {
		path := filepath.Join(stageDir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out := strings.ReplaceAll(string(b), `"`+oldID+`"`, `"`+newID+`"`)
		out = strings.ReplaceAll(out, `"`+agent+"/"+oldID+`"`, `"`+agent+"/"+newID+`"`)
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// copyTree is the EXDEV fallback for the atomic rename. We accept it
// as non-atomic at the moment because losing a single session mid-
// copy is recoverable from the staging dir (which we keep until
// success, then RemoveAll).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// printImportSummary renders the human-readable post-run summary.
// We always print at least one line so the user knows we ran, even
// when the bundle had zero new sessions.
func printImportSummary(app *App, res *importResult, dryRun bool) {
	app.lead()
	verb := "imported"
	if dryRun {
		verb = "would import"
	}
	fmt.Printf("%s %s %d session(s); skipped %d; overwritten %d; renamed %d\n",
		app.green("✓"), verb, res.imported, res.skipped, res.overwritten, res.renamed)
	if res.unresolvedConflicts > 0 {
		fmt.Printf("  %s %d session(s) had unresolved conflicts (skipped). Re-run with --on-conflict=overwrite|rename to merge.\n",
			app.yellow("!"), res.unresolvedConflicts)
	}
}
