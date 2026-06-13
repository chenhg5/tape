package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/chenhg5/tape/internal/agentid"
	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/export/snapshot"
	"github.com/chenhg5/tape/internal/redact"
)

func (a *App) archiveDir() string { return filepath.Join(a.home, "archive") }

// newExportCmd builds `tape export`: one command, one job — write a
// snapshot of the archive (or a filter-scoped slice of it) to a
// single file. We deliberately don't ship a `restore` companion or a
// `push --to <provider>` sub-tree: any tarball / zip is trivially
// inspectable with system tools, and "back up to cloud X" is the
// user's choice of cloud, not tape's job.
//
// --scan-only is the only non-write mode: it reuses the same
// walk + filter pipeline so "what would I be exporting?" stays
// consistent with "what was exported".
func newExportCmd(app *App) *cobra.Command {
	var (
		output, agent, dir, since, host          string
		excludeAgent, excludeDir, excludeHost    []string
		format, compress                         string
		splitBy, splitSize                       string
		jobs                                     int
		noRedact, dryRun, scanOnly               bool
	)
	cmd := &cobra.Command{
		Use:   "export [output]",
		Short: "Export the archive (or a filtered slice) as one artifact or a set of chunks",
		Long: `Writes a snapshot of the archive — full, or scoped by --agent / --dir
/ --host / --since — to one file (or a set of chunks; see below).
Choose the container with --format (tar or zip) and the codec with
--compress (zstd, gzip, xz, none). Defaults are tar + zstd, which is
the smallest + fastest combination.

Chunking: pass --split-by to produce a fan-out of artifacts instead
of one giant file. Useful when you want to upload incrementally,
shard backups across drives, or browse one agent at a time.

  --split-by agent    one file per agent (codex.tar.zst, cursor.tar.zst, …)
  --split-by month    one file per calendar month of session updated_at
  --split-by size     pack sessions in --split-size buckets (default 256M,
                      accepts K/M/G suffixes) — sessions are never split,
                      so the actual part size is "the last session before
                      the threshold", not exact bytes.

When chunking is on the resolved output path becomes a *prefix* and
each chunk's distinguishing suffix is inserted before the extension:
'tape-export-<ts>.<chunk>.tar.zst'. Pass -o to control the prefix.

Secrets in session.json are redacted on the way into the artifact
(replaced with [REDACTED:<rule>] for text, length-preserving masks
for binaries). Local archive files are never modified. Pass
--no-redact if you actually want the raw bytes — useful for round-
tripping into another tape install you fully control.

Pass --scan-only to list what would be redacted, without writing
anything. Same filter, same walk — handy for auditing.

Importing is not a tape feature: any tar.zst / .tar.gz / .zip is
extractable with system tools, and a future tape can pick up the
resulting files exactly where it left them.`,
		Example: `  tape export                                       # everything → tape-export-<ts>.tar.zst
  tape export snapshot.tar.gz --compress gzip       # explicit name + codec
  tape export --agent codex --since 7d              # codex, last week
  tape export --split-by agent                      # one file per agent
  tape export --split-by month --since 1y           # monthly chunks for the year
  tape export --split-by size --split-size 200M     # 200 MiB raw-byte buckets
  tape export --dir . --format zip --compress none  # current project as a .zip
  tape export --scan-only --agent claude-code       # audit secrets`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			run := startRun(app, "export")
			defer func() { err = run.finish(err) }()
			if len(args) == 1 {
				if output != "" {
					return usageErrf("specify the output either positionally or with --output, not both")
				}
				output = args[0]
			}
			// Pull configured defaults if the user didn't pass an
			// explicit value. cobra's `Changed` is the only honest
			// "did the user say so?" — relying on the string being
			// empty would punish people who legitimately want to set
			// it to "" via `tape config unset`.
			defaults := app.Defaults()
			if !cmd.Flag("format").Changed && defaults.Format != "" {
				format = defaults.Format
			}
			if !cmd.Flag("compress").Changed && defaults.Compress != "" {
				compress = defaults.Compress
			}
			if !cmd.Flag("jobs").Changed && defaults.Jobs > 0 {
				jobs = defaults.Jobs
			}
			if format == "" {
				format = snapshot.FormatTar
			}
			if compress == "" && format == snapshot.FormatTar {
				compress = snapshot.CompressZstd
			}
			if err := snapshot.ValidateFormat(format, compress); err != nil {
				return usageErrf("%v", err)
			}
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
			agentCanon, err := resolveAgentFilter(agent)
			if err != nil {
				return err
			}
			excludedAgents, err := resolveExcludeAgents(mergeExcludeAgents(app, excludeAgent))
			if err != nil {
				return err
			}
			filter := ports.Filter{
				Agent: agentCanon, Project: resolveDirFilter(dir),
				Host: host, Since: sinceTime,
				ExcludeAgents:   excludedAgents,
				ExcludeProjects: resolveExcludeDirs(mergeExcludeDirs(app, excludeDir)),
				ExcludeHosts:    splitCSVAndTrim(mergeExcludeHosts(app, excludeHost)),
			}
			run.setScope("agent", agentCanon)
			run.setScope("dir", dir)
			run.setScope("host", host)
			if len(excludedAgents) > 0 {
				run.setScope("exclude_agents", strings.Join(excludedAgents, ","))
			}
			run.setScope("split_by", splitBy)
			run.setScope("format", format)
			run.setScope("compress", compress)
			if scanOnly {
				run.setScope("mode", "scan-only")
				return runExportScan(cmd, app, filter)
			}

			if output == "" {
				output = snapshot.DefaultFilename(format, compress, time.Now())
			}

			split, err := parseSplit(splitBy, splitSize)
			if err != nil {
				return err
			}

			opts := ports.ExportOpts{
				ArchiveDir: app.archiveDir(),
				Filter:     filter,
				Format:     format,
				Compress:   compress,
				DryRun:     dryRun,
			}
			if !noRedact {
				opts.RedactCopy = redactArtifact
			}

			if split.mode == splitNone {
				// Single-stream export: spread the user's whole job
				// budget across one zstd encoder. Zero (the default)
				// = GOMAXPROCS, which is also klauspost/compress's
				// own default — we still set it explicitly so
				// `--jobs 1` works as "serial encoding" for
				// reproducible bench runs.
				opts.EncoderConcurrency = jobsToEncoderConcurrency(jobs, 1)
				return runExportSingle(cmd, app, opts, output, run)
			}
			return runExportChunked(cmd, app, opts, output, split, jobs, run)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path or chunk prefix (default: tape-export-<timestamp>.<ext>)")
	cmd.Flags().StringVar(&format, "format", "", "container format: tar (default) or zip")
	cmd.Flags().StringVar(&compress, "compress", "", "codec: zstd (default for tar), gzip, xz, none")
	cmd.Flags().StringVar(&agent, "agent", "", "only export sessions from this agent — full name or 2-letter shorthand ("+agentid.HelpLine()+")")
	cmd.Flags().StringVar(&dir, "dir", "", "only export sessions under this working dir ('.' = cwd)")
	cmd.Flags().StringVar(&host, "host", "", `only export from this origin host ("local" or ssh-host)`)
	cmd.Flags().StringArrayVar(&excludeAgent, "exclude-agent", nil, "exclude these agents (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeDir, "exclude-dir", nil, "exclude these project dirs (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeHost, "exclude-host", nil, `exclude these origin hosts ("local" = drop local-only; repeatable)`)
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().StringVar(&splitBy, "split-by", "", "split into chunks: none (default), size, agent, month")
	cmd.Flags().StringVar(&splitSize, "split-size", "256M", "chunk size budget when --split-by=size (K/M/G suffix accepted)")
	cmd.Flags().IntVarP(&jobs, "jobs", "j", 0, "parallelism budget: chunks run concurrently and zstd uses the remainder per chunk; 0 = auto (CPU count)")
	cmd.Flags().BoolVar(&noRedact, "no-redact", false, "keep secrets verbatim in the artifact")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing (exit 10 on success)")
	cmd.Flags().BoolVar(&scanOnly, "scan-only", false, "list secrets that would be redacted, write nothing")
	return cmd
}

// jobsToEncoderConcurrency derives the per-stream zstd worker count
// from the global --jobs budget and the number of chunk workers we
// plan to spawn in parallel.
//
//	jobs = 0  → auto: GOMAXPROCS / parallelChunks
//	jobs = N  → N / parallelChunks
//
// We always return at least 1 so zstd has someone to do the work.
// Splitting the budget keeps total threads ≈ jobs even when chunks
// run in parallel — over-subscription tanks throughput on archives
// the size of $TAPE_HOME.
func jobsToEncoderConcurrency(jobs, parallelChunks int) int {
	if parallelChunks < 1 {
		parallelChunks = 1
	}
	budget := jobs
	if budget <= 0 {
		budget = runtime.GOMAXPROCS(0)
	}
	per := budget / parallelChunks
	if per < 1 {
		per = 1
	}
	return per
}

// runExportSingle is the pre-chunking happy path: one Write call,
// one artifact, one result line. Kept as its own function so the
// chunked path doesn't have to special-case a 1-element loop.
//
// run is the oplog accumulator: we stash file count + byte total
// in it so `tape history` can summarize "what did this export
// actually emit?" without re-walking the artifact.
func runExportSingle(cmd *cobra.Command, app *App, opts ports.ExportOpts, output string, run *recordedRun) error {
	pb := app.newProgress("export", 0)
	opts.Output = output
	opts.OnProgress = func(done, total int64, path string) {
		if pb.total != total {
			pb.SetTotal(total)
		}
		pb.Update(done, path)
	}
	res, err := snapshot.Write(cmd.Context(), app.Archive(), opts)
	pb.Done("")
	if err != nil {
		return err
	}
	if run != nil {
		run.setCount("files", res.Changed)
		run.setCount("parts", 1)
		run.setBytes(res.Bytes)
	}
	if err := printExportResult(app, res); err != nil {
		return err
	}
	if opts.DryRun {
		return errDryRun
	}
	return nil
}

// splitMode enumerates the chunking strategies. Strings on purpose:
// they're flag values that travel through usage messages, JSON
// output, and tests.
type splitMode string

const (
	splitNone  splitMode = "none"
	splitSize  splitMode = "size"
	splitAgent splitMode = "agent"
	splitMonth splitMode = "month"
)

// splitSpec is the parsed form of --split-by + --split-size.
// sizeBudget is only meaningful when mode == splitSize; we still
// parse it eagerly so the user's typo on the size suffix surfaces
// before we walk a 5 GB archive.
type splitSpec struct {
	mode       splitMode
	sizeBudget int64
}

func parseSplit(by, size string) (splitSpec, error) {
	m := splitMode(strings.TrimSpace(strings.ToLower(by)))
	switch m {
	case "", splitNone:
		return splitSpec{mode: splitNone}, nil
	case splitAgent, splitMonth:
		return splitSpec{mode: m}, nil
	case splitSize:
		bytes, err := parseSize(size)
		if err != nil {
			return splitSpec{}, usageErrf("invalid --split-size %q: %v", size, err)
		}
		if bytes <= 0 {
			return splitSpec{}, usageErrf("--split-size must be > 0")
		}
		return splitSpec{mode: splitSize, sizeBudget: bytes}, nil
	default:
		return splitSpec{}, usageErrf("--split-by %q: must be none, size, agent or month", by)
	}
}

// parseSize accepts "200", "200K", "200M", "200G" (case-insensitive,
// IEC powers of 1024). No fractions and no spaces — the goal is to
// reject typos noisily, not to be DSL-flexible.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1 << 30
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not a number")
	}
	return n * mult, nil
}

// runExportChunked plans the chunk list against the resolved Filter,
// then walks it serially calling snapshot.Write once per chunk with
// KeepIDs pinned. Each chunk's output path inserts a per-chunk
// suffix before the extension, so a prefix of
// "tape-export-20260613-093000.tar.zst" becomes
// "tape-export-20260613-093000.codex.tar.zst" etc.
//
// We deliberately don't parallelize: the writers are I/O-bound and
// share an output directory, and a serial loop gives a predictable
// progress bar and easy ctrl-C semantics. If users ever need
// throughput we can revisit.
func runExportChunked(cmd *cobra.Command, app *App, opts ports.ExportOpts, prefix string, split splitSpec, jobs int, run *recordedRun) error {
	sums, err := app.Archive().List(cmd.Context(), opts.Filter)
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		return ErrNoResults
	}
	var sizeOf func(string) int64
	if split.mode == splitSize {
		sizes, err := scanSessionSizes(opts.ArchiveDir)
		if err != nil {
			return err
		}
		sizeOf = func(id string) int64 { return sizes[id] }
	}
	chunks := planChunks(sums, split, sizeOf)
	if len(chunks) == 0 {
		return ErrNoResults
	}
	if app.debug {
		for _, ch := range chunks {
			app.debugf("chunk %s: %d session(s)", ch.suffix, len(ch.ids))
		}
	}

	// Worker count = min(--jobs, number of chunks). When --jobs is 0
	// (default) we use GOMAXPROCS. Capping at len(chunks) avoids
	// the silly case of "16 workers fighting for 3 files of work".
	parallel := jobs
	if parallel <= 0 {
		parallel = runtime.GOMAXPROCS(0)
	}
	if parallel > len(chunks) {
		parallel = len(chunks)
	}
	if parallel < 1 {
		parallel = 1
	}
	// Split the encoding thread budget across the active chunk
	// writers so the kernel never schedules N*N goroutines fighting
	// for CPU. See jobsToEncoderConcurrency.
	opts.EncoderConcurrency = jobsToEncoderConcurrency(jobs, parallel)

	type partResult struct {
		Output string `json:"output"`
		Format string `json:"format"`
		Files  int    `json:"changed_files"`
		Bytes  int64  `json:"bytes,omitempty"`
		Chunk  string `json:"chunk"`
		Note   string `json:"note,omitempty"`
	}
	results := make([]partResult, len(chunks))

	// Parallel writers share the archive (read-only) and write to
	// distinct output paths, so the only contention is CPU + disk.
	// We keep results[] indexed by chunk position so JSON output
	// stays stable regardless of completion order.
	//
	// Progress output is chunk-granular here, not per-file: with N
	// parallel writers a per-file bar would flicker between chunks
	// and tell the user nothing. We print "[i/N] chunk done" lines
	// behind a mutex instead, which is calm and informative on
	// every terminal — including dumb / piped stderr.
	var (
		group, ctx = errgroup.WithContext(cmd.Context())
		printMu    sync.Mutex
		done       int
	)
	group.SetLimit(parallel)
	for i, ch := range chunks {
		i, ch := i, ch
		group.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := insertSuffix(prefix, ch.suffix)
			copy := opts
			copy.Output = path
			copy.KeepIDs = ch.ids
			copy.Filter = ports.Filter{}
			copy.OnProgress = nil
			res, err := snapshot.Write(ctx, app.Archive(), copy)
			if err != nil {
				return fmt.Errorf("chunk %s: %w", ch.suffix, err)
			}
			results[i] = partResult{
				Output: res.Output, Format: res.Format, Files: res.Changed,
				Bytes: res.Bytes, Chunk: ch.suffix, Note: res.Note,
			}
			if !app.useJSON() {
				printMu.Lock()
				done++
				fmt.Fprintf(os.Stderr, "  %s [%d/%d] %s  %s\n",
					app.green("✓"), done, len(chunks),
					app.cyan(filepath.Base(res.Output)),
					app.gray(humanSize(res.Bytes)))
				printMu.Unlock()
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	var totalBytes int64
	var totalFiles int
	for _, r := range results {
		totalBytes += r.Bytes
		totalFiles += r.Files
	}
	if run != nil {
		run.setCount("parts", len(results))
		run.setCount("files", totalFiles)
		run.setCount("workers", parallel)
		run.setBytes(totalBytes)
	}
	if app.useJSON() {
		if err := emitJSON(map[string]any{
			"split": map[string]any{
				"mode": string(split.mode), "size_budget": split.sizeBudget,
				"jobs": parallel, "encoder_concurrency": opts.EncoderConcurrency,
			},
			"parts": results, "count": len(results),
		}); err != nil {
			return err
		}
	} else {
		if len(results) > 1 {
			fmt.Fprintf(os.Stderr, "  %s %d chunk(s), %s total · %d worker(s)\n",
				app.gray("Σ"), len(results), humanSize(totalBytes), parallel)
		}
	}
	if opts.DryRun {
		return errDryRun
	}
	return nil
}

// chunk pairs a per-chunk filename suffix (sans extension) with the
// session IDs that should land in it. We pre-resolve the set so
// every snapshot.Write call below is a leaf operation — no extra
// archive queries inside.
type chunk struct {
	suffix string
	ids    []string
}

// planChunks turns a sorted summary list into chunk descriptors per
// strategy:
//   - agent: group by Summary.Agent, suffix = agent name
//   - month: group by YYYY-MM of UpdatedAt, suffix = "YYYY-MM"
//   - size:  greedy pack — sessions in (sorted) order, breaking
//            whenever adding the next would exceed sizeBudget;
//            actual size will under- or overshoot the budget by one
//            session because we don't split a session across
//            archives. Suffix = "part-001" zero-padded for natural
//            ls ordering.
func planChunks(sums []model.Summary, split splitSpec, sizeOf func(string) int64) []chunk {
	switch split.mode {
	case splitAgent:
		groups := map[string][]string{}
		order := []string{}
		for _, s := range sums {
			if _, ok := groups[s.Agent]; !ok {
				order = append(order, s.Agent)
			}
			groups[s.Agent] = append(groups[s.Agent], s.ID)
		}
		sort.Strings(order)
		out := make([]chunk, 0, len(order))
		for _, agent := range order {
			out = append(out, chunk{suffix: agent, ids: groups[agent]})
		}
		return out
	case splitMonth:
		groups := map[string][]string{}
		order := []string{}
		for _, s := range sums {
			key := s.UpdatedAt.UTC().Format("2006-01")
			if _, ok := groups[key]; !ok {
				order = append(order, key)
			}
			groups[key] = append(groups[key], s.ID)
		}
		sort.Strings(order)
		out := make([]chunk, 0, len(order))
		for _, key := range order {
			out = append(out, chunk{suffix: key, ids: groups[key]})
		}
		return out
	case splitSize:
		// Order by ID for determinism (UpdatedAt is the natural
		// alternative but is sometimes equal across sessions in
		// fixtures). Greedy-pack into buckets.
		ids := make([]model.Summary, len(sums))
		copy(ids, sums)
		sort.SliceStable(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
		var parts []chunk
		var bucket []string
		var bucketBytes int64
		for _, s := range ids {
			var sz int64
			if sizeOf != nil {
				sz = sizeOf(s.ID)
			}
			if sz <= 0 {
				sz = 1 // conservative: always count *something*
			}
			if len(bucket) > 0 && bucketBytes+sz > split.sizeBudget {
				parts = append(parts, chunk{suffix: partLabel(len(parts) + 1), ids: bucket})
				bucket, bucketBytes = nil, 0
			}
			bucket = append(bucket, s.ID)
			bucketBytes += sz
		}
		if len(bucket) > 0 {
			parts = append(parts, chunk{suffix: partLabel(len(parts) + 1), ids: bucket})
		}
		return parts
	}
	return nil
}

// partLabel pads to three digits so the chunks sort lexicographically
// in `ls`. We won't realistically hit 1000 chunks (that would mean a
// terabyte of sessions at default size), but the cost of three-digit
// labels is zero and the cost of fixing it later isn't.
func partLabel(n int) string { return fmt.Sprintf("part-%03d", n) }

// scanSessionSizes walks the archive once and returns the raw (pre-
// compression) byte total per session id. We use this only for size-
// based chunking so we can decide where to break without opening
// each session twice. The map key is "<agent>/<source-id>", matching
// what model.Summary.ID returns.
//
// Walking the whole tree once is cheaper than asking the archive
// layer to do per-session size lookups (which would re-stat every
// file in every call). Cost: one filepath.WalkDir of ~tens of
// thousands of small files in the worst case — fine for an
// interactive command.
func scanSessionSizes(archiveDir string) (map[string]int64, error) {
	sizes := map[string]int64{}
	err := filepath.WalkDir(archiveDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == archiveDir {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(archiveDir, path)
		if err != nil {
			return err
		}
		// rel = "<agent>/<project>/<sid>/<file>"; collapse the
		// project segment to derive the canonical session id
		// the planner uses.
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 4 {
			return nil
		}
		id := parts[0] + "/" + parts[2]
		info, err := d.Info()
		if err != nil {
			return err
		}
		sizes[id] += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return sizes, nil
	}
	return sizes, err
}

// insertSuffix injects ".<suffix>" before the file's multi-component
// extension. "tape-export-20260613.tar.zst" + "codex" →
// "tape-export-20260613.codex.tar.zst". Recognized multi-component
// extensions: .tar.zst / .tar.gz / .tar.xz. Anything else uses the
// single-component extension semantics.
func insertSuffix(path, suffix string) string {
	dir, name := filepath.Split(path)
	for _, ext := range []string{".tar.zst", ".tar.gz", ".tar.xz"} {
		if strings.HasSuffix(name, ext) {
			base := strings.TrimSuffix(name, ext)
			return filepath.Join(dir, base+"."+suffix+ext)
		}
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return filepath.Join(dir, base+"."+suffix+ext)
}

// redactArtifact is the default mask function: length-preserving on
// the SQLite DB (so the file structure isn't broken) and readable
// markers everywhere else. Same policy the old `tape backup export`
// applied; lifted into its own function so the export pipeline and
// any future caller can reuse it without dragging in a Backup type.
func redactArtifact(path string, data []byte) []byte {
	if filepath.Ext(path) == ".db" {
		return redact.ApplyKeepLength(data)
	}
	return redact.Apply(data)
}

// runExportScan implements --scan-only: walk the archive scoped by
// the user's filter, return the secret findings without writing
// anything. We don't fail-exit on findings; this is an *audit* mode,
// not a gate. The real protection is the default redactor that runs
// on every actual export.
func runExportScan(cmd *cobra.Command, app *App, filter ports.Filter) error {
	findings, err := snapshot.Scan(cmd.Context(), app.Archive(),
		ports.ExportOpts{ArchiveDir: app.archiveDir(), Filter: filter},
		func(path string, data []byte) []snapshot.Finding {
			rs := redact.Scan(path, data)
			out := make([]snapshot.Finding, len(rs))
			for i, f := range rs {
				out[i] = snapshot.Finding{
					Rule: f.Rule, Path: f.Path, Line: f.Line, Preview: f.Preview,
				}
			}
			return out
		})
	if err != nil {
		return err
	}
	if app.useJSON() {
		return emitJSON(map[string]any{
			"findings": findings, "count": len(findings),
		})
	}
	app.lead()
	if len(findings) == 0 {
		fmt.Printf("%s no secrets found in scope; export would carry redactions for: nothing\n", app.green("✓"))
		return nil
	}
	fmt.Printf("%s %s\n",
		app.yellow("!"),
		app.bold(fmt.Sprintf("%d potential secret(s) in scope (would be redacted on export):", len(findings))))
	for _, f := range findings {
		fmt.Printf("  %s %s:%d  %s\n",
			app.yellow(padRightDisp(f.Rule, 18)),
			app.cyan(f.Path),
			f.Line,
			app.gray(truncDisp(collapseWhitespace(f.Preview), 60)))
	}
	return nil
}

func printExportResult(app *App, res *ports.ExportResult) error {
	if app.useJSON() {
		return emitJSON(map[string]any{"result": res})
	}
	app.lead()
	fmt.Printf("%s %s %s",
		app.green("✓"),
		app.cyan(res.Output),
		app.bold(fmt.Sprintf("· %d file(s)", res.Changed)))
	if res.Bytes > 0 {
		fmt.Printf("  %s", app.gray(humanSize(res.Bytes)))
	}
	if res.Note != "" {
		fmt.Printf("\n  %s", app.gray(res.Note))
	}
	fmt.Println()
	return nil
}

// humanSize renders an int64 byte count as a short human string.
// We don't pull in a unit library for this — IEC powers of two with
// a single-decimal cut is what every "ls -lh" style expects.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
