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
	// Host scopes results to sessions mirrored from a particular SSH
	// host. The sentinel "local" matches only sessions parsed off this
	// machine (Summary.Host == ""). Empty string means "no host filter".
	Host   string
	Since  time.Time
	Limit  int
	Offset int
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
	// Host follows the same sentinel rules as Filter.Host: "" means
	// no scope, "local" means Host == "", anything else is an exact match.
	Host    string
	Since   time.Time
	Limit   int
	Offset  int
	// Sort selects the order of results:
	//   "" / "recent"   — newest session first, in-session by message ts ASC
	//   "relevance"     — BM25 across all messages (long sessions win)
	// Default is "recent" because users usually want "the conversation I had
	// just now" rather than "the one that mentions the term the most".
	Sort string
}

type Hit struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	// Host is "" for local sessions, non-empty (e.g. "dev@build-01")
	// for sessions mirrored in via `tape sync --remote`. Surfaced in
	// the JSON contract so script consumers can split local/remote
	// without an extra archive lookup, and used by the picker to tag
	// remote rows and route resume through SSH.
	Host      string    `json:"host,omitempty"`
	Title     string    `json:"title,omitempty"`
	Project   string    `json:"project"`
	MessageID string    `json:"message_id,omitempty"`
	Role      string    `json:"role"`
	Snippet   string    `json:"snippet"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// Index provides full-text search over archived sessions. It is fully
// rebuildable from the Archive and is never part of exports.
type Index interface {
	Upsert(ctx context.Context, s *model.Session) error
	Search(ctx context.Context, q Query) ([]Hit, error)
	Close() error
}

// ExportOpts describes one `tape export` invocation. The defaults
// (zero Filter, no Output, RedactCopy nil) export every archived
// session as a tar.zst on stdout — but the CLI always sets Output,
// so that path is mostly here for tests.
//
// Filter follows the same shape ls/search use, so users can scope
// exports the way they already scope listings (--agent, --dir,
// --since, --host). RedactCopy is wired by the CLI when --no-redact
// is *off* (the default); the snapshot writer otherwise passes file
// bytes through unchanged.
//
// KeepIDs is the explicit-whitelist escape hatch the CLI's chunked
// export modes lean on (--split-by agent/month/size). When non-nil
// it takes precedence over Filter for the "which sessions go in"
// decision; Filter still applies for everything else. We keep
// Filter around because most exports never chunk and the simpler
// path stays simpler.
type ExportOpts struct {
	ArchiveDir string
	Output     string // local file path
	Filter     Filter
	KeepIDs    []string // canonical "<agent>/<source-id>" form
	Format     string   // "tar" or "zip"
	Compress   string   // "zstd" | "gzip" | "xz" | "none"; only meaningful for tar
	DryRun     bool
	// RedactCopy, when non-nil, transforms file contents on the way
	// into the artifact. Local archive files are never modified.
	RedactCopy func(path string, data []byte) []byte
	// OnProgress fires per file written. total may be -1 when unknown.
	OnProgress func(done, total int64, path string)
}

// ExportResult is what the CLI prints after a successful export, plus
// what shows up in `tape export --json`. Changed counts files written
// (or scanned in --dry-run); Bytes is the on-disk artifact size.
type ExportResult struct {
	Output  string `json:"output,omitempty"`
	Format  string `json:"format"`
	Changed int    `json:"changed_files"`
	Bytes   int64  `json:"bytes,omitempty"`
	Note    string `json:"note,omitempty"`
}

// SessionWriter is implemented by sources that can also write a session in
// their native format, enabling native cross-agent restore.
type SessionWriter interface {
	// Write materializes s as a new session in this agent's storage and
	// returns the command the user runs to resume it.
	Write(ctx context.Context, s *model.Session) (resumeCmd string, err error)
}
