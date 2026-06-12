// Package ports defines the capability interfaces of tape's hexagonal core.
// Adapters (sources, archive backends, indexes, ...) implement these; the
// core never imports an adapter.
package ports

import (
	"context"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// SessionRef is a cheap handle to a session discovered on disk, before
// parsing. Files lists every file the session consists of (source of truth
// for checksums and raw archiving).
type SessionRef struct {
	Agent     string
	SourceID  string
	Files     []string
	UpdatedAt time.Time
}

func (r SessionRef) ID() string { return r.Agent + "/" + r.SourceID }

// Source reads sessions from one agent's native storage.
type Source interface {
	// Name returns the agent slug, e.g. "claude-code".
	Name() string
	// Detect reports whether this agent has data on this machine.
	Detect(ctx context.Context) (found bool, dataDir string, err error)
	// List discovers sessions updated at or after since (zero = all).
	List(ctx context.Context, since time.Time) ([]SessionRef, error)
	// Load parses one session into the normalized model.
	Load(ctx context.Context, ref SessionRef) (*model.Session, error)
}

type Filter struct {
	Agent   string
	Project string // matches project slug prefix or cwd substring
	Since   time.Time
	Limit   int
}

// Archive is the durable session store: byte-for-byte raw copies plus the
// normalized IR. Everything else (index, memory) is derived from it.
type Archive interface {
	// Stale reports whether ref's content differs from what is archived,
	// returning the current content checksum.
	Stale(ref SessionRef) (stale bool, checksum string, err error)
	// Put stores the session and raw copies of ref.Files.
	Put(ctx context.Context, s *model.Session, ref SessionRef, checksum string) error
	Get(ctx context.Context, id string) (*model.Session, error)
	List(ctx context.Context, f Filter) ([]model.Summary, error)
}

type Query struct {
	Text    string
	Agent   string
	Project string
	Since   time.Time
	Limit   int
}

type Hit struct {
	SessionID string    `json:"session_id"`
	Agent     string    `json:"agent"`
	Title     string    `json:"title,omitempty"`
	Project   string    `json:"project"`
	MessageID string    `json:"message_id,omitempty"`
	Role      string    `json:"role"`
	Snippet   string    `json:"snippet"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// Index provides full-text search over archived sessions. It is fully
// rebuildable from the Archive and is never part of backups.
type Index interface {
	Upsert(ctx context.Context, s *model.Session) error
	Search(ctx context.Context, q Query) ([]Hit, error)
	Close() error
}

type BackupOpts struct {
	ArchiveDir  string
	Destination string // git remote URL, tarball path, ... target-specific
	Message     string
	DryRun      bool
	// RedactCopy, when non-nil, transforms file contents on the way into
	// the backup artifact. Local archive files are never modified.
	RedactCopy func(path string, data []byte) []byte
}

type BackupResult struct {
	Target  string `json:"target"`
	Action  string `json:"action"`
	Changed int    `json:"changed_files"`
	Ref     string `json:"ref,omitempty"`
	Output  string `json:"output,omitempty"`
	Note    string `json:"note,omitempty"`
}

// BackupTarget moves the archive to/from external storage.
type BackupTarget interface {
	Name() string
	Push(ctx context.Context, opts BackupOpts) (*BackupResult, error)
	Pull(ctx context.Context, opts BackupOpts) (*BackupResult, error)
}

// SessionWriter is implemented by sources that can also write a session in
// their native format, enabling native cross-agent restore.
type SessionWriter interface {
	// Write materializes s as a new session in this agent's storage and
	// returns the command the user runs to resume it.
	Write(ctx context.Context, s *model.Session) (resumeCmd string, err error)
}
