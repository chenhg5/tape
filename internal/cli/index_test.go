package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/index/sqlitefts"
)

// nonMaintainerIndex satisfies ports.Index but NOT indexMaintainer,
// to cover the "test fake without SessionCount" branch in
// indexLooksEmpty (returns false so callers fall through to
// generic ErrNoResults rather than fabricate a hint).
type nonMaintainerIndex struct{}

func (nonMaintainerIndex) Upsert(context.Context, *model.Session) error { return nil }
func (nonMaintainerIndex) Search(context.Context, ports.Query) ([]ports.Hit, error) {
	return nil, nil
}
func (nonMaintainerIndex) Close() error { return nil }

func TestNoResultsHintBuildsCliError(t *testing.T) {
	err := noResultsHint("nothing matched", "try tape sync")
	var ce cliError
	if !errors.As(err, &ce) {
		t.Fatalf("noResultsHint did not return a cliError: %T", err)
	}
	if ce.Type != "no_results" {
		t.Errorf("Type = %q, want no_results", ce.Type)
	}
	if ce.Message != "nothing matched" || ce.Suggestion != "try tape sync" {
		t.Errorf("payload = %+v", ce)
	}
	// Must still be treated as ErrNoResults so cli.go keeps exit code 3.
	if !errors.Is(err, ErrNoResults) {
		t.Errorf("noResultsHint() should errors.Is(_, ErrNoResults)")
	}
}

func TestIndexLooksEmptyReportsTrueForFreshIndex(t *testing.T) {
	ix, err := sqlitefts.Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	if !indexLooksEmpty(context.Background(), ix) {
		t.Fatal("fresh sqlitefts index should look empty")
	}
}

func TestIndexLooksEmptyReportsFalseAfterUpsert(t *testing.T) {
	ix, err := sqlitefts.Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	now := time.Now().UTC()
	sess := &model.Session{
		ID: "claude-code/x", Agent: "claude-code", SourceID: "x",
		Title: "t", CWD: "/tmp", StartedAt: now, UpdatedAt: now,
	}
	if err := ix.Upsert(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if indexLooksEmpty(context.Background(), ix) {
		t.Fatal("populated index should not look empty")
	}
}

func TestIndexLooksEmptyFalseForNonMaintainer(t *testing.T) {
	// Test fakes that only implement ports.Index (no SessionCount) must
	// not trigger the "empty index" hint — we have no way to tell, so
	// stay conservative and fall through to ErrNoResults.
	if indexLooksEmpty(context.Background(), nonMaintainerIndex{}) {
		t.Fatal("non-maintainer index should not be reported as empty")
	}
}

func TestNoResultsForSearchEmptyIndexAttachesHint(t *testing.T) {
	ix, err := sqlitefts.Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	err = noResultsForSearch(context.Background(), ix)
	var ce cliError
	if !errors.As(err, &ce) || ce.Suggestion == "" {
		t.Fatalf("expected cliError with suggestion, got %v (%T)", err, err)
	}
	if !errors.Is(err, ErrNoResults) {
		t.Fatal("noResultsForSearch on empty index should still be ErrNoResults")
	}
}

func TestNoResultsForSearchNonEmptyReturnsBareErrNoResults(t *testing.T) {
	ix, err := sqlitefts.Open(filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	now := time.Now().UTC()
	if err := ix.Upsert(context.Background(), &model.Session{
		ID: "cc/y", Agent: "claude-code", SourceID: "y",
		Title: "t", CWD: "/tmp", StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	err = noResultsForSearch(context.Background(), ix)
	if err != ErrNoResults {
		t.Fatalf("populated index: want bare ErrNoResults, got %v", err)
	}
}

func TestNoResultsForLsEmptyArchiveAttachesHint(t *testing.T) {
	app := &App{home: t.TempDir()}

	err := noResultsForLs(context.Background(), app)
	var ce cliError
	if !errors.As(err, &ce) || ce.Suggestion == "" {
		t.Fatalf("expected cliError with suggestion, got %v (%T)", err, err)
	}
	if !errors.Is(err, ErrNoResults) {
		t.Fatal("noResultsForLs on empty archive should still be ErrNoResults")
	}
}

func TestNoResultsForLsNonEmptyArchiveReturnsBareErrNoResults(t *testing.T) {
	app := &App{home: t.TempDir()}
	arc := app.Archive()

	now := time.Now().UTC()
	sess := &model.Session{
		ID: "cc/z", Agent: "claude-code", SourceID: "z",
		Title: "t", CWD: "/tmp", StartedAt: now, UpdatedAt: now,
	}
	ref := ports.SessionRef{Agent: sess.Agent, SourceID: sess.SourceID, UpdatedAt: now}
	if err := arc.Put(context.Background(), sess, ref, "deadbeef"); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	err := noResultsForLs(context.Background(), app)
	if err != ErrNoResults {
		t.Fatalf("populated archive: want bare ErrNoResults, got %v", err)
	}
}
