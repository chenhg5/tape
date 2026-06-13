// Package mimo reads Xiaomi MiMo Code sessions. MiMo Code is a fork
// of sst/opencode (same Drizzle session/message/part schema, even the
// `mimo import` command accepts opncd.ai share links), so we delegate
// to the opencode parser and only own the path discovery + the agent
// relabel — the same pattern qoder uses on top of qwen.
//
// Storage layout (per https://mimo.xiaomi.com/mimocode/sessions and
// /troubleshooting):
//
//	$MIMOCODE_HOME/data/mimocode.db          ← when MIMOCODE_HOME is set
//	$XDG_DATA_HOME/mimocode/mimocode.db
//	~/.local/share/mimocode/mimocode.db      ← Linux / macOS default
//	~/.local/share/mimocode/storage/mimocode.db   ← layout variant
//
// We probe these in order at construction time and stop on the first
// regular file that exists.
package mimo

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/opencode"
)

const agentName = "mimocode"

type Source struct {
	dbPath string
	inner  *opencode.Source
}

func New(home string) *Source {
	db := resolveDBPath(home)
	return &Source{
		dbPath: db,
		inner:  opencode.NewWith(db, agentName),
	}
}

// resolveDBPath walks the candidate paths in priority order. We stop
// at the first existing regular file; if none exist we still return
// the highest-priority candidate so Detect() reports a deterministic
// "not present" path the user can inspect.
func resolveDBPath(home string) string {
	candidates := candidatePaths(home)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

func candidatePaths(home string) []string {
	var out []string
	if mh := os.Getenv("MIMOCODE_HOME"); mh != "" {
		out = append(out, filepath.Join(mh, "data", "mimocode.db"))
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "mimocode", "mimocode.db"))
		out = append(out, filepath.Join(xdg, "mimocode", "storage", "mimocode.db"))
	}
	out = append(out, filepath.Join(home, ".local", "share", "mimocode", "mimocode.db"))
	out = append(out, filepath.Join(home, ".local", "share", "mimocode", "storage", "mimocode.db"))
	return out
}

func (s *Source) Name() string { return agentName }

func (s *Source) Detect(ctx context.Context) (bool, string, error) {
	return s.inner.Detect(ctx)
}

func (s *Source) List(ctx context.Context, since time.Time) ([]ports.SessionRef, error) {
	return s.inner.List(ctx, since)
}

func (s *Source) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	return s.inner.Load(ctx, ref)
}
