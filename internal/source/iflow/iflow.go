// Package iflow reads iFlow CLI sessions from ~/.iflow/{projects,conversations}.
//
// iFlow is a Gemini-CLI fork that kept the original gemini-cli storage
// shape (JSONL with MessageRecord + $set / $rewindTo patches). Because of
// that, we delegate parsing to the gemini package and only customize
// the discovery paths and agent name. If iFlow ever diverges we'll grow
// a dedicated parser; for now this is the cheapest path to "just works".
package iflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/gemini"
)

const agentName = "iflow"

type Source struct {
	root   string              // ~/.iflow
	inner  *gemini.Source      // does the heavy lifting via shared schema
	rooted map[string]struct{} // pinned roots so List doesn't escape
}

func New(home string) *Source {
	root := filepath.Join(home, ".iflow")
	return &Source{
		root:  root,
		inner: gemini.New(""), // unused; we override List & Load
	}
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	if _, err := os.Stat(s.root); err != nil {
		return false, "", nil
	}
	for _, sub := range []string{"projects", "conversations", "chats"} {
		if _, err := os.Stat(filepath.Join(s.root, sub)); err == nil {
			return true, s.root, nil
		}
	}
	return false, "", nil
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	patterns := []string{
		filepath.Join(s.root, "projects", "*", "session-*.jsonl"),
		filepath.Join(s.root, "projects", "*", "chats", "session-*.jsonl"),
		filepath.Join(s.root, "projects", "*", "*.jsonl"),
		filepath.Join(s.root, "conversations", "conversation-*.json"),
		filepath.Join(s.root, "conversations", "*.jsonl"),
		filepath.Join(s.root, "chats", "*.jsonl"),
	}
	seen := map[string]bool{}
	var refs []ports.SessionRef
	for _, p := range patterns {
		files, _ := filepath.Glob(p)
		for _, f := range files {
			if seen[f] {
				continue
			}
			seen[f] = true
			st, err := os.Stat(f)
			if err != nil || st.Size() == 0 {
				continue
			}
			if !since.IsZero() && st.ModTime().Before(since) {
				continue
			}
			refs = append(refs, ports.SessionRef{
				Agent:     agentName,
				SourceID:  sessionID(f),
				Files:     []string{f},
				UpdatedAt: st.ModTime().UTC(),
			})
		}
	}
	return refs, nil
}

func sessionID(p string) string {
	stem := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".jsonl"), ".json")
	stem = strings.TrimPrefix(stem, "session-")
	stem = strings.TrimPrefix(stem, "conversation-")
	// Keep the trailing slug after the last dash to match Gemini-style
	// "session-<ts>-<8hex>" naming when present.
	if i := strings.LastIndex(stem, "-"); i > 0 && len(stem)-i-1 >= 6 {
		return stem[i+1:]
	}
	return stem
}

// Load reuses the Gemini parser by forging a SessionRef with the iflow
// agent name swapped in after the fact. The Gemini parser doesn't read
// the agent field, so the only thing we need to override is the
// resulting Session.ID/Agent/SourceID.
func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	// Delegate to gemini's Load by transmuting the ref.
	geminiRef := ports.SessionRef{
		Agent:     "gemini",
		SourceID:  ref.SourceID,
		Files:     ref.Files,
		UpdatedAt: ref.UpdatedAt,
	}
	sess, err := s.inner.Load(ctx, geminiRef)
	if err != nil || sess == nil {
		return sess, err
	}
	sess.Agent = agentName
	sess.ID = agentName + "/" + ref.SourceID
	return sess, nil
}
