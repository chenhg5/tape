package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/oplog"
)

// newHistoryCmd surfaces the operations log (~/.tape/operations.log)
// as a human table or as JSON. The table prints newest-first because
// that's what users want when they ask "did sync just succeed?";
// JSON output preserves the same order plus carries the raw
// timestamps + scope/counts maps for scripts.
//
// We don't expose any way to trim or rotate the log from this command
// — that belongs to `tape uninstall` (which clears the whole file)
// or to a future explicit `tape history --clear`. The minimum
// affordance is "see what tape did"; mutating that surface would
// invite trust questions ("did history get edited?") we don't need.
func newHistoryCmd(app *App) *cobra.Command {
	var limit int
	var op string
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent tape operations (sync / export / restore / update)",
		Long: `Reads ~/.tape/operations.log (one JSONL line per invocation, written
by sync / export / restore / update) and renders it newest-first.

The log is opt-out via TAPE_NO_OPLOG=1; if you've had it set you'll
see fewer rows than you expect, which is the whole point.

Filter with --op {sync|export|restore|update} when you only care
about one verb (e.g. "what did the last three syncs do?"). The JSON
form carries scope, counts, bytes, exit_code and the raw RFC3339
timestamps — agents can chain it through jq without parsing the
table.`,
		Example: `  tape history
  tape history --limit 5
  tape history --op sync --json | jq '.[] | {ts: .started, archived: .counts.archived}'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if oplog.Disabled() {
				app.debugf("oplog is disabled via %s; existing records still shown", oplog.EnvDisable)
			}
			records, err := oplog.Read(app.home, 0)
			if err != nil {
				return err
			}
			if op != "" {
				filtered := records[:0]
				for _, r := range records {
					if r.Op == op {
						filtered = append(filtered, r)
					}
				}
				records = filtered
			}
			// Sort newest-first regardless of on-disk order; Append
			// writes append-only so this is normally already sorted,
			// but we don't rely on it.
			sort.Slice(records, func(i, j int) bool {
				return records[i].Started.After(records[j].Started)
			})
			if limit > 0 && len(records) > limit {
				records = records[:limit]
			}
			if app.useJSON() {
				if records == nil {
					records = []oplog.Record{}
				}
				return emitJSON(map[string]any{"records": records, "count": len(records)})
			}
			if len(records) == 0 {
				app.lead()
				fmt.Printf("  %s no operations logged yet\n", app.gray("·"))
				return nil
			}
			renderHistory(app, records)
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "show only the most recent N entries (0 = all)")
	cmd.Flags().StringVar(&op, "op", "", "filter by operation (sync, export, restore, update)")
	return cmd
}

// renderHistory prints one row per record: timestamp + op + outcome
// glyph + scope/counts summary. Width is wide enough to read in a
// typical terminal but not so dense that ssh-on-mobile users have to
// squint. Outcome glyphs match the rest of the CLI (✓ ok, ! error,
// · dry-run).
func renderHistory(app *App, records []oplog.Record) {
	app.lead()
	for _, r := range records {
		mark := app.green("✓")
		switch {
		case r.Err != "":
			mark = app.red("!")
		case r.ExitCode == ExitDryRunOK:
			mark = app.gray("·")
		}
		when := r.Started.Local().Format("2006-01-02 15:04")
		fmt.Printf("  %s %s  %s",
			mark, app.gray(when),
			app.bold(padRightDisp(r.Op, 8)))
		if r.Duration != "" {
			fmt.Printf("  %s", app.gray(padRightDisp(r.Duration, 8)))
		}
		if summary := summarizeRecord(r); summary != "" {
			fmt.Printf("  %s", summary)
		}
		fmt.Println()
		if r.Err != "" {
			fmt.Printf("    %s %s\n", app.red("·"), r.Err)
		}
	}
}

// summarizeRecord turns the open-ended Scope+Counts+Bytes into one
// terse human line. We don't try to be clever — sort keys, print
// "k=v" pairs, comma-separate. The order is "scope first, then
// counts, then bytes" so the line reads like a thought: "what slice
// did you ask for, and what came back".
func summarizeRecord(r oplog.Record) string {
	parts := []string{}
	scopeKeys := sortedKeys(r.Scope)
	for _, k := range scopeKeys {
		v := r.Scope[k]
		if v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	countKeys := sortedCountKeys(r.Counts)
	for _, k := range countKeys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, r.Counts[k]))
	}
	if r.Bytes > 0 {
		parts = append(parts, humanSize(r.Bytes))
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// recordedRun is the helper RunE wrappers use to bracket a command:
// stamp the start time, run the body, append an oplog record with
// the outcome. Returns the original error untouched so the cobra
// exit-code mapping in Execute keeps working.
//
// Callers shape the Scope / Counts / Bytes themselves before /
// after the run — we don't try to introspect because the right
// summary is op-specific (sync wants archived/skipped, export
// wants chunk count + bytes, restore wants strategy).
type recordedRun struct {
	app     *App
	op      string
	scope   map[string]string
	counts  map[string]int
	bytes   int64
	started time.Time
}

func startRun(app *App, op string) *recordedRun {
	return &recordedRun{
		app: app, op: op, started: time.Now(),
		scope: map[string]string{}, counts: map[string]int{},
	}
}

func (r *recordedRun) setScope(k, v string) {
	if v == "" {
		return
	}
	r.scope[k] = v
}
func (r *recordedRun) setCount(k string, v int) { r.counts[k] = v }
func (r *recordedRun) setBytes(v int64)         { r.bytes = v }

// finish stamps the run, appends to the oplog, and returns runErr
// untouched. The oplog write itself is silent — failure to record
// must never mask the real outcome.
func (r *recordedRun) finish(runErr error) error {
	rec := oplog.Record{
		Op: r.op, Started: r.started, Finished: time.Now(),
		Scope: r.scope, Counts: r.counts, Bytes: r.bytes,
	}
	switch {
	case runErr == nil:
		rec.ExitCode = ExitOK
	case isUsageError(runErr) || isCobraUsage(runErr):
		rec.ExitCode = ExitUsage
		rec.Err = runErr.Error()
	default:
		// errDryRun and ErrNoResults flow through Execute() and get
		// mapped there; classify them here too so history matches.
		switch runErr.Error() {
		case "dry run":
			rec.ExitCode = ExitDryRunOK
		case "no results":
			rec.ExitCode = ExitNoResults
		default:
			rec.ExitCode = ExitError
			rec.Err = runErr.Error()
		}
	}
	_ = oplog.Append(r.app.home, rec) // best-effort: never block on disk
	return runErr
}
