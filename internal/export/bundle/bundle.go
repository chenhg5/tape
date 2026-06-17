// Package bundle defines the on-disk manifest tape stamps into every
// export bundle (tape-bundle.json at the bundle root). The manifest
// turns an opaque tar/zip into a self-describing share artifact: the
// importing side can answer "what's in here, which agent produced
// each session, what cwd was it captured under, has it changed since"
// without unpacking the entire bundle.
//
// Compatibility:
//
//   - SchemaVersion is bumped only when we make a breaking change to
//     the field layout. Readers that don't recognize a SchemaVersion
//     they don't understand should bail out gracefully (see Read).
//   - Bundles produced by tape <= 0.2.0 do NOT carry a manifest — the
//     import path falls back to scanning the archive layout
//     (`<agent>/<project>/<sid>/...`). That fallback lives in the
//     import command, not here, so this package stays a pure data
//     format with no I/O against the archive directory.
//
// Persistence is plain JSON — small, diff-friendly, lets users grep
// `jq '.sessions[].title' tape-bundle.json` without any tooling.
package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// SchemaV1 is the first (and current) bundle manifest schema version.
const SchemaV1 = 1

// ManifestFilename is the conventional path of the manifest inside a
// bundle. Always a bundle-root entry; never nested.
const ManifestFilename = "tape-bundle.json"

// Kind classifies the bundle's intent. Today the import + share
// commands only differentiate display strings off this value, but
// having it on disk lets future tooling decide e.g. "share bundles
// should preserve cwd" vs. "backup bundles should restore cwd".
type Kind string

const (
	KindShare  Kind = "share"
	KindBackup Kind = "backup"
)

// Manifest is the top-level on-disk object.
type Manifest struct {
	SchemaVersion int          `json:"schema_version"`
	TapeVersion   string       `json:"tape_version"`
	Kind          Kind         `json:"kind"`
	CreatedAt     time.Time    `json:"created_at"`
	Source        SourceInfo   `json:"source"`
	Sessions      []SessionRow `json:"sessions"`
}

// SourceInfo records who produced the bundle so the importer can
// attribute the data and so the human reader knows where it came from.
type SourceInfo struct {
	Host     string `json:"host,omitempty"`
	TapeHome string `json:"tape_home,omitempty"`
}

// SessionRow is one entry under sessions[]. It mirrors the subset of
// model.Session a downstream importer needs to make routing decisions
// without parsing the entire session.json on disk.
type SessionRow struct {
	ID          string    `json:"id"`
	Agent       string    `json:"agent"`
	SourceID    string    `json:"source_id"`
	ProjectSlug string    `json:"project_slug,omitempty"`
	OriginalCWD string    `json:"original_cwd,omitempty"`
	Title       string    `json:"title,omitempty"`
	MsgCount    int       `json:"msg_count,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	// Checksum is a content fingerprint of the session payload —
	// today populated from local archive meta.json when available.
	// Empty checksums are valid (the importer just can't short-circuit
	// "same content already present" decisions).
	Checksum string `json:"checksum,omitempty"`
}

// RowFromSession builds a SessionRow off a fully-loaded model.Session.
// Callers (export, share) use this so the manifest stays consistent
// with what the archive stores.
func RowFromSession(s *model.Session, checksum string) SessionRow {
	return SessionRow{
		ID:          s.ID,
		Agent:       s.Agent,
		SourceID:    s.SourceID,
		ProjectSlug: model.ProjectSlug(s.CWD),
		OriginalCWD: s.CWD,
		Title:       s.Title,
		MsgCount:    len(s.Messages),
		StartedAt:   s.StartedAt,
		UpdatedAt:   s.UpdatedAt,
		Checksum:    checksum,
	}
}

// Write serializes a manifest as indented JSON. Indented (not
// minimized) because manifests are small and human-readability is the
// whole point of having one on disk.
func Write(w io.Writer, m *Manifest) error {
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaV1
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// Read parses a manifest off any io.Reader and validates the schema
// version. We reject SchemaVersion 0 (no manifest at all is a separate
// code path — the importer's "old layout fallback") and versions
// strictly greater than the latest we know about, because they may
// carry fields whose semantics we haven't been taught yet and
// silently ignoring them could lose data.
func Read(r io.Reader) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(r)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if m.SchemaVersion == 0 {
		return nil, ErrMissingSchemaVersion
	}
	if m.SchemaVersion > SchemaV1 {
		return nil, fmt.Errorf("%w: manifest schema_version=%d, this tape knows up to %d (run `tape update`)",
			ErrUnsupportedSchema, m.SchemaVersion, SchemaV1)
	}
	return &m, nil
}

// Sentinels surfaced to callers; the import command maps them onto
// user-friendly errors with suggestion text.
var (
	// ErrMissingSchemaVersion is returned when a JSON parses cleanly
	// but lacks schema_version. Old (v0.2.x) bundles trip this; the
	// importer uses it as the signal to switch to layout-scan fallback.
	ErrMissingSchemaVersion = errors.New("bundle manifest has no schema_version")

	// ErrUnsupportedSchema is returned for a schema_version newer
	// than the running tape supports.
	ErrUnsupportedSchema = errors.New("unsupported bundle schema_version")
)
