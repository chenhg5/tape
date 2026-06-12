// Package service contains the use-case orchestration of tape's core.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/chenhg5/tape/internal/core/ports"
)

type SyncReport struct {
	Sources []SourceReport `json:"sources"`
}

type SourceReport struct {
	Agent    string   `json:"agent"`
	Found    bool     `json:"found"`
	DataDir  string   `json:"data_dir,omitempty"`
	Scanned  int      `json:"scanned"`
	Archived int      `json:"archived"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

func (r SyncReport) Archived() int {
	n := 0
	for _, s := range r.Sources {
		n += s.Archived
	}
	return n
}

func (r SyncReport) Skipped() int {
	n := 0
	for _, s := range r.Sources {
		n += s.Skipped
	}
	return n
}

func (r SyncReport) ErrorCount() int {
	n := 0
	for _, s := range r.Sources {
		n += len(s.Errors)
	}
	return n
}

type Sync struct {
	Sources []ports.Source
	Archive ports.Archive
	Index   ports.Index
}

// Run archives new or changed sessions from every detected source.
// A failing source or session never aborts the rest; failures are
// collected into the report.
func (s *Sync) Run(ctx context.Context, since time.Time) (SyncReport, error) {
	var report SyncReport
	for _, src := range s.Sources {
		report.Sources = append(report.Sources, s.runSource(ctx, src, since))
	}
	return report, ctx.Err()
}

func (s *Sync) runSource(ctx context.Context, src ports.Source, since time.Time) SourceReport {
	rep := SourceReport{Agent: src.Name()}
	found, dir, err := src.Detect(ctx)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("detect: %v", err))
		return rep
	}
	rep.Found, rep.DataDir = found, dir
	if !found {
		return rep
	}
	refs, err := src.List(ctx, since)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("list: %v", err))
		return rep
	}
	rep.Scanned = len(refs)
	for _, ref := range refs {
		if ctx.Err() != nil {
			return rep
		}
		if err := s.syncOne(ctx, src, ref, &rep); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", ref.ID(), err))
		}
	}
	return rep
}

func (s *Sync) syncOne(ctx context.Context, src ports.Source, ref ports.SessionRef, rep *SourceReport) error {
	stale, checksum, err := s.Archive.Stale(ref)
	if err != nil {
		return err
	}
	if !stale {
		rep.Skipped++
		return nil
	}
	sess, err := src.Load(ctx, ref)
	if err != nil {
		return err
	}
	if sess == nil || len(sess.Messages) == 0 {
		rep.Skipped++ // empty session (e.g. opened but never used)
		return nil
	}
	if err := s.Archive.Put(ctx, sess, ref, checksum); err != nil {
		return err
	}
	if err := s.Index.Upsert(ctx, sess); err != nil {
		return fmt.Errorf("index: %w", err)
	}
	rep.Archived++
	return nil
}
