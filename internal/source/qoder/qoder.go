// Package qoder reads Qoder CLI sessions from
// ~/.qoder/projects/<project>/<sessionId>.jsonl.
//
// Qoder is Alibaba's closed-source successor to iFlow CLI (iFlow shut
// down 2026-04-17 and the official migration target is Qoder). Both are
// downstream from Qwen Code's tree, so their transcripts use the same
// ChatRecord JSONL schema (uuid/parentUuid/sessionId/type/message.parts).
// We therefore delegate parsing to the qwen package and only own the
// discovery paths and the relabel.
//
// On disk, each session lives as a pair of files in the project dir:
//
//	~/.qoder/projects/<project-slug>/<sessionId>.jsonl          ← transcript
//	~/.qoder/projects/<project-slug>/<sessionId>-session.json   ← metadata
//	~/.qoder/projects/<project-slug>/<sessionId>/state.json     ← state
//
// We treat the .jsonl as the source of truth (`-session.json` is just
// the directory index Qoder's `/resume` uses) and ignore subdirs.
package qoder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/qwen"
)

const agentName = "qoder"

type Source struct {
	root  string       // ~/.qoder
	inner *qwen.Source // does the heavy lifting via the shared ChatRecord schema
}

func New(home string) *Source {
	return &Source{
		root:  filepath.Join(home, ".qoder"),
		inner: qwen.New(""), // never used to discover; only Load() is delegated
	}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.root); err != nil {
		return false, "", nil
	}
	if _, err := os.Stat(filepath.Join(s.root, "projects")); err == nil {
		return true, s.root, nil
	}
	return false, "", nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	pattern := filepath.Join(s.root, "projects", "*", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var refs []ports.SessionRef
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		// Skip Qoder's per-session sidecars — only the bare transcript
		// is a real conversation file.
		if strings.HasSuffix(stem, "-session") {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			continue
		}
		if !since.IsZero() && st.ModTime().Before(since) {
			continue
		}
		refs = append(refs, ports.SessionRef{
			Agent:     agentName,
			SourceID:  stem,
			Files:     []string{f},
			UpdatedAt: st.ModTime().UTC(),
		})
	}
	return refs, nil
}

// Load borrows qwen's ChatRecord parser then relabels the result so the
// session ends up in the qoder shelf of the archive.
func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	qwenRef := ports.SessionRef{
		Agent:     "qwen",
		SourceID:  ref.SourceID,
		Files:     ref.Files,
		UpdatedAt: ref.UpdatedAt,
	}
	sess, err := s.inner.Load(ctx, qwenRef)
	if err != nil || sess == nil {
		return sess, err
	}
	sess.Agent = agentName
	sess.ID = agentName + "/" + ref.SourceID
	return sess, nil
}
