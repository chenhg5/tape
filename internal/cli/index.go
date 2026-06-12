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
