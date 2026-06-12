package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
)

func newIndexCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Manage the search index",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "rebuild",
		Short:   "Rebuild the search index from the archive",
		Long:    "Walks every archived session and re-indexes it. The index is fully derived; run this after `tape backup pull` or if search misbehaves.",
		Example: "  tape index rebuild",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := app.rebuildIndex(cmd)
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(map[string]any{"indexed": n})
			}
			fmt.Printf("indexed %d session(s)\n", n)
			return nil
		},
	})
	return cmd
}

func (a *App) rebuildIndex(cmd *cobra.Command) (int, error) {
	ix, err := a.Index()
	if err != nil {
		return 0, err
	}
	sums, err := a.Archive().List(cmd.Context(), ports.Filter{})
	if err != nil {
		return 0, err
	}
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
	}
	return n, nil
}
