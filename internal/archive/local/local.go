// Package local implements ports.Archive on the local filesystem.
//
// Layout (under root, default ~/.tape/archive):
//
//	<agent>/<project-slug>/<source-id>/
//	    raw/...           byte-for-byte copies of the source files
//	    session.json      normalized IR (regenerable from raw)
//	    meta.json         checksum, source paths, summary fields
//
// raw/ is the only non-regenerable part; backups only need raw + meta.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lukechampine.com/blake3"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

const metaSchemaVersion = 1

type Archive struct {
	root string
}

func New(root string) *Archive { return &Archive{root: root} }

type meta struct {
	SchemaVersion int           `json:"schema_version"`
	Checksum      string        `json:"checksum"`
	SourceFiles   []string      `json:"source_files"`
	ArchivedAt    time.Time     `json:"archived_at"`
	Summary       model.Summary `json:"summary"`
}

func (a *Archive) Stale(ref ports.SessionRef) (bool, string, error) {
	sum, err := checksumFiles(ref.Files)
	if err != nil {
		return false, "", err
	}
	m, err := a.readMeta(a.dirOf(ref.Agent, ref.SourceID))
	if err != nil {
		return true, sum, nil // not archived yet (or unreadable meta -> re-archive)
	}
	return m.Checksum != sum, sum, nil
}

func (a *Archive) Put(ctx context.Context, s *model.Session, ref ports.SessionRef, checksum string) error {
	dir := filepath.Join(a.root, s.Agent, model.ProjectSlug(s.CWD), s.SourceID)
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0o700); err != nil {
		return err
	}
	for _, f := range ref.Files {
		if err := copyFile(f, filepath.Join(dir, "raw", filepath.Base(f))); err != nil {
			return fmt.Errorf("copy raw %s: %w", f, err)
		}
	}
	if err := writeJSON(filepath.Join(dir, "session.json"), s); err != nil {
		return err
	}
	m := meta{
		SchemaVersion: metaSchemaVersion,
		Checksum:      checksum,
		SourceFiles:   ref.Files,
		ArchivedAt:    time.Now().UTC(),
		Summary:       s.Summary(),
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), m); err != nil {
		return err
	}
	// The same source session may move between project slugs if normalization
	// improves; drop stale copies under other slugs.
	a.dropDuplicates(s.Agent, s.SourceID, dir)
	return nil
}

func (a *Archive) Get(ctx context.Context, id string) (*model.Session, error) {
	agent, sourceID, ok := strings.Cut(id, "/")
	if !ok {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	dir := a.dirOf(agent, sourceID)
	if dir == "" {
		return nil, fmt.Errorf("session %q not found", id)
	}
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, err
	}
	var s model.Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *Archive) List(ctx context.Context, f ports.Filter) ([]model.Summary, error) {
	var out []model.Summary
	err := a.walkSessions(func(dir string) error {
		m, err := a.readMeta(dir)
		if err != nil {
			return nil // tolerate broken entries
		}
		if f.Agent != "" && m.Summary.Agent != f.Agent {
			return nil
		}
		if f.Project != "" && !matchProject(m.Summary, f.Project) {
			return nil
		}
		if !f.Since.IsZero() && m.Summary.UpdatedAt.Before(f.Since) {
			return nil
		}
		out = append(out, m.Summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Resolve expands a session id prefix or suffix into a full unique id.
func (a *Archive) Resolve(ctx context.Context, idOrPrefix string) (string, error) {
	all, err := a.List(ctx, ports.Filter{})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, s := range all {
		if s.ID == idOrPrefix {
			return s.ID, nil
		}
		if strings.Contains(s.ID, idOrPrefix) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matches %q", idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q is ambiguous (%d matches, e.g. %s)", idOrPrefix, len(matches), matches[0])
	}
}

func matchProject(s model.Summary, q string) bool {
	slug := model.ProjectSlug(q)
	return strings.HasPrefix(s.Project, slug) || strings.Contains(s.CWD, q)
}

// dirOf finds the session directory regardless of project slug.
func (a *Archive) dirOf(agent, sourceID string) string {
	base := filepath.Join(a.root, agent)
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name(), sourceID)
		if _, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil {
			return dir
		}
	}
	return ""
}

func (a *Archive) dropDuplicates(agent, sourceID, keep string) {
	base := filepath.Join(a.root, agent)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name(), sourceID)
		if dir != keep {
			if _, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil {
				os.RemoveAll(dir)
			}
		}
	}
}

func (a *Archive) walkSessions(fn func(dir string) error) error {
	return filepath.WalkDir(a.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // empty archive
			}
			return err
		}
		if d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		return fn(filepath.Dir(path))
	})
}

func (a *Archive) readMeta(dir string) (meta, error) {
	var m meta
	if dir == "" {
		return m, fs.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

// checksumFiles hashes every file's content with blake3 and combines the
// per-file digests into one stable checksum.
func checksumFiles(files []string) (string, error) {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	combined := blake3.New(32, nil)
	for _, f := range sorted {
		h := blake3.New(32, nil)
		r, err := os.Open(f)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, r)
		r.Close()
		if err != nil {
			return "", err
		}
		combined.Write(h.Sum(nil))
	}
	return fmt.Sprintf("%x", combined.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
