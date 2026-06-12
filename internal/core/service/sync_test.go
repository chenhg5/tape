package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

type fakeSource struct {
	name      string
	found     bool
	listErr   error
	refs      []ports.SessionRef
	sessions  map[string]*model.Session // by SourceID; nil entry = empty session
	loadErrOn string
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Detect(context.Context) (bool, string, error) {
	return f.found, "/data/" + f.name, nil
}
func (f *fakeSource) List(context.Context, time.Time) ([]ports.SessionRef, error) {
	return f.refs, f.listErr
}
func (f *fakeSource) Load(_ context.Context, ref ports.SessionRef) (*model.Session, error) {
	if ref.SourceID == f.loadErrOn {
		return nil, errors.New("parse boom")
	}
	return f.sessions[ref.SourceID], nil
}

type fakeArchive struct {
	known map[string]bool // checksum already stored
	puts  []string
	fail  string
}

func (a *fakeArchive) Stale(ref ports.SessionRef) (bool, string, error) {
	return !a.known[ref.ID()], "sum-" + ref.SourceID, nil
}
func (a *fakeArchive) Put(_ context.Context, s *model.Session, ref ports.SessionRef, _ string) error {
	if s.SourceID == a.fail {
		return errors.New("disk full")
	}
	a.puts = append(a.puts, s.ID)
	return nil
}
func (a *fakeArchive) Get(context.Context, string) (*model.Session, error) { return nil, nil }
func (a *fakeArchive) List(context.Context, ports.Filter) ([]model.Summary, error) {
	return nil, nil
}

type fakeIndex struct {
	upserts []string
	failOn  string
}

func (i *fakeIndex) Upsert(_ context.Context, s *model.Session) error {
	if s.SourceID == i.failOn {
		return errors.New("index boom")
	}
	i.upserts = append(i.upserts, s.ID)
	return nil
}
func (i *fakeIndex) Search(context.Context, ports.Query) ([]ports.Hit, error) { return nil, nil }
func (i *fakeIndex) Close() error                                             { return nil }

func ref(agent, id string) ports.SessionRef {
	return ports.SessionRef{Agent: agent, SourceID: id}
}

func sess(agent, id string, msgs int) *model.Session {
	s := &model.Session{ID: agent + "/" + id, Agent: agent, SourceID: id}
	for i := 0; i < msgs; i++ {
		s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Text: "x"})
	}
	return s
}

func TestSyncHappyPath(t *testing.T) {
	src := &fakeSource{
		name: "codex", found: true,
		refs:     []ports.SessionRef{ref("codex", "a"), ref("codex", "b")},
		sessions: map[string]*model.Session{"a": sess("codex", "a", 2), "b": sess("codex", "b", 1)},
	}
	arch, ix := &fakeArchive{known: map[string]bool{}}, &fakeIndex{}
	rep, err := (&Sync{Sources: []ports.Source{src}, Archive: arch, Index: ix}).Run(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	sr := rep.Sources[0]
	if sr.Scanned != 2 || sr.Archived != 2 || sr.Skipped != 0 || len(sr.Errors) != 0 {
		t.Errorf("report: %+v", sr)
	}
	if len(arch.puts) != 2 || len(ix.upserts) != 2 {
		t.Errorf("puts=%v upserts=%v", arch.puts, ix.upserts)
	}
	if rep.Archived() != 2 {
		t.Errorf("total archived = %d", rep.Archived())
	}
}

func TestSyncSkipsUnchangedAndEmpty(t *testing.T) {
	src := &fakeSource{
		name: "codex", found: true,
		refs: []ports.SessionRef{ref("codex", "known"), ref("codex", "empty")},
		sessions: map[string]*model.Session{
			"empty": sess("codex", "empty", 0), // parsed but no messages
		},
	}
	arch := &fakeArchive{known: map[string]bool{"codex/known": true}}
	ix := &fakeIndex{}
	rep, _ := (&Sync{Sources: []ports.Source{src}, Archive: arch, Index: ix}).Run(context.Background(), time.Time{})
	sr := rep.Sources[0]
	if sr.Skipped != 2 || sr.Archived != 0 {
		t.Errorf("report: %+v", sr)
	}
}

// One broken session must not block the rest of the batch, and one broken
// source must not block other sources.
func TestSyncFailureIsolation(t *testing.T) {
	bad := &fakeSource{
		name: "claude-code", found: true,
		refs:      []ports.SessionRef{ref("claude-code", "boom"), ref("claude-code", "ok")},
		sessions:  map[string]*model.Session{"ok": sess("claude-code", "ok", 1)},
		loadErrOn: "boom",
	}
	deadSource := &fakeSource{name: "cursor", found: true, listErr: errors.New("db locked")}
	notInstalled := &fakeSource{name: "codex", found: false}

	arch, ix := &fakeArchive{known: map[string]bool{}}, &fakeIndex{}
	rep, err := (&Sync{Sources: []ports.Source{bad, deadSource, notInstalled}, Archive: arch, Index: ix}).
		Run(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sources[0].Archived != 1 || len(rep.Sources[0].Errors) != 1 {
		t.Errorf("bad source report: %+v", rep.Sources[0])
	}
	if !strings.Contains(rep.Sources[0].Errors[0], "boom") {
		t.Errorf("error must identify the session: %v", rep.Sources[0].Errors)
	}
	if len(rep.Sources[1].Errors) != 1 || !strings.Contains(rep.Sources[1].Errors[0], "list") {
		t.Errorf("dead source report: %+v", rep.Sources[1])
	}
	if rep.Sources[2].Found {
		t.Error("not-installed source must report found=false")
	}
}

func TestSyncIndexErrorReported(t *testing.T) {
	src := &fakeSource{
		name: "codex", found: true,
		refs:     []ports.SessionRef{ref("codex", "a")},
		sessions: map[string]*model.Session{"a": sess("codex", "a", 1)},
	}
	arch := &fakeArchive{known: map[string]bool{}}
	ix := &fakeIndex{failOn: "a"}
	rep, _ := (&Sync{Sources: []ports.Source{src}, Archive: arch, Index: ix}).Run(context.Background(), time.Time{})
	sr := rep.Sources[0]
	if sr.Archived != 0 || len(sr.Errors) != 1 || !strings.Contains(sr.Errors[0], "index") {
		t.Errorf("report: %+v", sr)
	}
}

func TestSyncContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &fakeSource{name: "codex", found: true, refs: []ports.SessionRef{ref("codex", "a")}}
	rep, err := (&Sync{Sources: []ports.Source{src}, Archive: &fakeArchive{known: map[string]bool{}}, Index: &fakeIndex{}}).
		Run(ctx, time.Time{})
	if err == nil {
		t.Error("cancelled context must surface an error")
	}
	if rep.Archived() != 0 {
		t.Errorf("archived after cancel: %d", rep.Archived())
	}
}
