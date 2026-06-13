// Package oplog records what tape actually did, one JSONL line per
// invocation, to a single rolling file at ~/.tape/operations.log.
// It exists so users (and agents) can answer "what happened last
// time I ran sync?" without piecing it together from shell history,
// and so post-mortems on a "where did my archive grow" question
// don't have to be guesswork.
//
// Design constraints:
//   - **Append-only** and crash-safe enough that interrupted writes
//     don't poison earlier records. We open/append/close per call
//     and never seek; partial last-line damage is acceptable (Read
//     skips unparseable trailing junk).
//   - **Cheap.** Recording a sync run shouldn't add measurable
//     latency. JSON encoding + one O_APPEND write is ~milliseconds.
//   - **Opt-out via env**, mirroring the convention mole, gh, and
//     most modern CLIs use: TAPE_NO_OPLOG=1 silences every Append.
//   - **No PII beyond what the user has already typed.** The Scope
//     map carries flag values (agent name, host name, since), never
//     session contents or message text.
//
// This package owns the on-disk format. Callers should never read or
// write operations.log directly; that future-proofs us against
// rotating to SQLite or per-day shards if the file ever gets unwieldy.
package oplog

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FileName is the basename used inside the tape home directory.
// Kept exported so tests and `tape uninstall` know where to look
// without re-deriving it.
const FileName = "operations.log"

// EnvDisable is the kill switch. Set to "1" / "true" / anything non-
// empty other than "0" / "false" and Append becomes a no-op. Read
// still works, so users can still inspect history they recorded
// previously.
const EnvDisable = "TAPE_NO_OPLOG"

// Record is the on-disk shape. Field choices are intentional:
//   - Op identifies the verb ("sync", "export", "restore", "update").
//     Subverbs we care about would become "sync.install" etc.
//   - Started / Finished bracket the operation; Duration is computed
//     so consumers don't have to.
//   - Scope is the "what slice" — flag values that influenced the
//     run. Strings only so JSON stays predictable.
//   - Counts is the "what happened" — small integer outcomes
//     (archived, skipped, parts, files). Open-ended so different ops
//     can use whatever keys make sense.
//   - Bytes is for size-flavored ops (export). Zero is fine.
//   - Err / ExitCode capture failure; an empty Err with non-zero
//     ExitCode is allowed for dry-run successes (exit 10).
//
// Adding fields is safe (older Read() unmarshals will ignore them);
// renaming or removing fields requires bumping a schema indicator
// — for now we lean on "open-ended Scope/Counts maps cover most
// growth without a schema bump".
type Record struct {
	Op       string            `json:"op"`
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished"`
	Duration string            `json:"duration"`
	Scope    map[string]string `json:"scope,omitempty"`
	Counts   map[string]int    `json:"counts,omitempty"`
	Bytes    int64             `json:"bytes,omitempty"`
	Err      string            `json:"error,omitempty"`
	ExitCode int               `json:"exit_code"`
}

// Path returns the absolute oplog path under the given tape home.
// We don't create directories here; the caller (Append) is the only
// writer and it ensures the dir exists on demand.
func Path(home string) string { return filepath.Join(home, FileName) }

// Disabled reports whether the env kill switch is on. Exported so
// the cli debugf path can tell users "skipped: TAPE_NO_OPLOG set".
func Disabled() bool {
	v := os.Getenv(EnvDisable)
	switch v {
	case "", "0", "false", "FALSE", "False":
		return false
	default:
		return true
	}
}

// Append writes one record to the oplog. It silently returns nil
// when disabled — Append's contract is "do your best, never block
// the caller's real work", so callers can sprinkle it into RunE
// without worrying about failure handling.
//
// The Duration field is filled in for the caller (no point making
// every site format it the same way), and Finished defaults to
// time.Now() if the caller didn't set it.
func Append(home string, rec Record) error {
	if Disabled() {
		return nil
	}
	if rec.Finished.IsZero() {
		rec.Finished = time.Now()
	}
	if !rec.Started.IsZero() {
		rec.Duration = rec.Finished.Sub(rec.Started).Round(time.Millisecond).String()
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(Path(home), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(rec)
}

// Read returns the most recent N records (oldest first within the
// returned slice). limit <= 0 returns every record. Missing file is
// not an error — it just means "no history yet" — so the calling
// CLI can stay simple.
//
// Implementation note: we read the whole file. Tape operations are
// rare enough that an oplog growing past a few MB would mean the
// user is hammering tape, in which case a one-shot read is still
// trivial.
func Read(home string, limit int) ([]Record, error) {
	f, err := os.Open(Path(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	var out []Record
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec Record
			if jerr := json.Unmarshal(line, &rec); jerr == nil && rec.Op != "" {
				out = append(out, rec)
			}
			// otherwise: corrupt / partial line; skip silently so a
			// truncated last write doesn't break history view.
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
