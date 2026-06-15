package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/index/sqlitefts"
)

// indexMaintainer is satisfied by sqlitefts.Index. It is declared as a
// minimal interface so that test fakes for ports.Index don't have to
// implement these methods — sync only acts on the maintainer surface
// when it is actually available.
type indexMaintainer interface {
	WantsRebuild(ctx context.Context) (bool, error)
	SessionCount(ctx context.Context) (int, error)
	Reset(ctx context.Context) error
	MarkBuilt(ctx context.Context) error
}

// noResultsHint returns a "no results" error that ALSO carries a
// human-readable suggestion. Used by search / ls when they detect that
// the empty result set is symptomatic of an empty/unbuilt index — most
// commonly a fresh install where the user hasn't run `tape sync` yet,
// or a leftover stale db from before a schema-breaking upgrade.
//
// The Suggestion is shown in human mode (stderr) and serialised in
// --json mode. The exit code stays at 3 because cliError.Is satisfies
// errors.Is(_, ErrNoResults).
func noResultsHint(message, suggestion string) error {
	return cliError{Type: "no_results", Message: message, Suggestion: suggestion}
}

// noResultsForLs is the ls-side specialisation of noResultsHint. Where
// search asks the index, ls asks the archive: when the unfiltered
// archive count is 0, the user hasn't synced anything yet and a
// filter-by-filter "no results" message would obscure that. When the
// archive has data but the current filters return nothing, fall back
// to generic ErrNoResults so the existing exit-3 contract holds.
func noResultsForLs(ctx context.Context, app *App) error {
	total, err := app.Archive().Count(ctx, ports.Filter{})
	if err == nil && total == 0 {
		return noResultsHint(
			"no archived sessions yet",
			"run `tape sync` to import sessions from your installed agents",
		)
	}
	return ErrNoResults
}

// noResultsForSearch is the search-side specialisation of noResultsHint:
// it differentiates a real "you queried for X and nothing matched"
// outcome (generic ErrNoResults) from the "you have nothing indexed yet"
// case (suggest tape sync). The same shape works for ls.
func noResultsForSearch(ctx context.Context, ix ports.Index) error {
	if indexLooksEmpty(ctx, ix) {
		return noResultsHint(
			"no results — your search index is empty",
			"run `tape sync` to archive your agent sessions, then search again",
		)
	}
	return ErrNoResults
}

// indexLooksEmpty asks the index how many sessions it currently holds.
// Returns true when the count is 0 OR the index doesn't expose the
// SessionCount method (test fakes) — in the fake case we conservatively
// fall through to the generic "no results" path. Errors querying the
// index are treated as "not empty" so we don't fabricate a misleading
// hint on top of a real read failure.
func indexLooksEmpty(ctx context.Context, ix ports.Index) bool {
	m, ok := ix.(indexMaintainer)
	if !ok {
		return false
	}
	n, err := m.SessionCount(ctx)
	if err != nil {
		return false
	}
	return n == 0
}

func newIndexCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "index",
		Short:  "Maintenance for the search index (advanced)",
		Hidden: true, // tape sync keeps the index in sync; this is escape-hatch only
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "rebuild",
		Short:   "Rebuild the search index from the archive",
		Long:    "Walks every archived session and re-indexes it. Tape sync runs this automatically when the indexer schema changes; only use it as an escape hatch.",
		Example: "  tape index rebuild",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := app.Index()
			if err != nil {
				return err
			}
			if m, ok := ix.(indexMaintainer); ok {
				if err := m.Reset(cmd.Context()); err != nil {
					return err
				}
			}
			n, err := app.rebuildIndex(cmd, ix)
			if err != nil {
				return err
			}
			if m, ok := ix.(indexMaintainer); ok {
				if err := m.MarkBuilt(cmd.Context()); err != nil {
					return err
				}
			}
			if app.useJSON() {
				return emitJSON(map[string]any{"indexed": n})
			}
			app.lead()
			fmt.Printf("indexed %d session(s)\n", n)
			return nil
		},
	})
	return cmd
}

// maybeRebuildIndex transparently rebuilds the search index when it is
// out of sync with the archive. Three triggers, all silent if no work is
// needed:
//
//  1. The on-disk index marker is older than the current indexer
//     (schema bump). The whole table is wiped and refilled.
//  2. The archive has more sessions than the index. Index was deleted
//     or hand-edited; we refill it.
//
// Stays a no-op when the archive is empty or the index is up to date.
// User only ever sees a "re-indexing" progress line.
func (a *App) maybeRebuildIndex(cmd *cobra.Command, ix ports.Index) error {
	m, ok := ix.(indexMaintainer)
	if !ok {
		return nil
	}
	sums, err := a.Archive().List(cmd.Context(), ports.Filter{})
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		return nil // nothing to index yet
	}
	need, err := m.WantsRebuild(cmd.Context())
	if err != nil {
		return err
	}
	if !need {
		indexed, err := m.SessionCount(cmd.Context())
		if err != nil {
			return err
		}
		if indexed >= len(sums) {
			return nil
		}
	}
	if err := m.Reset(cmd.Context()); err != nil {
		return err
	}
	if _, err := a.rebuildIndex(cmd, ix); err != nil {
		return err
	}
	return m.MarkBuilt(cmd.Context())
}

func (a *App) rebuildIndex(cmd *cobra.Command, ix ports.Index) (int, error) {
	sums, err := a.Archive().List(cmd.Context(), ports.Filter{})
	if err != nil {
		return 0, err
	}
	pb := a.newProgress("re-indexing", int64(len(sums)))
	defer pb.Done("")
	n := 0
	for _, sum := range sums {
		s, err := a.Archive().Get(cmd.Context(), sum.ID)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: %v\n", sum.ID, err)
			continue
		}
		if err := ix.Upsert(cmd.Context(), s); err != nil {
			return n, err
		}
		n++
		pb.Update(int64(n), sum.ID)
	}
	return n, nil
}

// Keep sqlitefts imported so the interface assertion above type-checks
// even if no other file references the package directly in the future.
var _ indexMaintainer = (*sqlitefts.Index)(nil)
