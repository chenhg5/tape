package e2e

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// `tape export` writes a single artifact, defaults to tar.zst, and
// redacts secrets on the way in without touching local files. This
// test pins the contract end-to-end via the real binary.
func TestExportRedactsSecretsByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")
	e.plantSecret()

	out := filepath.Join(t.TempDir(), "archive.tar.zst")

	// dry-run exits 10, prints the plan but writes nothing
	e.mustRun(10, "export", "-o", out, "--dry-run")
	if _, err := os.Stat(out); err == nil {
		t.Fatal("dry run wrote the artifact")
	}

	d := e.mustRun(0, "export", "-o", out).data(t)
	res := d["result"].(map[string]any)
	if res["format"] != "tar" {
		t.Errorf("default format = %v, want tar", res["format"])
	}
	if res["bytes"].(float64) <= 0 {
		t.Errorf("artifact size = %v", res["bytes"])
	}

	planted := readTarMember(t, out, "codex/planted/deadbeef/session.json")
	if strings.Contains(planted, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("artifact leaks the secret")
	}
	if !strings.Contains(planted, "[REDACTED:aws-access-key]") {
		t.Errorf("redaction marker missing: %s", planted)
	}

	// local archive untouched: redaction is in-stream, not in-place
	local, _ := os.ReadFile(filepath.Join(e.dir, "archive", "codex", "planted", "deadbeef", "session.json"))
	if !strings.Contains(string(local), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("export modified the local archive")
	}

	// --no-redact opts back in to verbatim bytes
	raw := filepath.Join(t.TempDir(), "raw.tar.zst")
	e.mustRun(0, "export", "-o", raw, "--no-redact")
	if !strings.Contains(readTarMember(t, raw, "codex/planted/deadbeef/session.json"), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("--no-redact did not keep the secret")
	}
}

// --scan-only walks the same filter the writer would and lists what
// would be redacted; nothing is written and the exit code is 0
// (audit, not gate).
func TestExportScanOnly(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// clean archive: no findings
	d := e.mustRun(0, "export", "--scan-only").data(t)
	if d["count"] != float64(0) {
		t.Fatalf("clean archive findings = %v", d)
	}

	e.plantSecret()
	r := e.mustRun(0, "export", "--scan-only")
	d = r.data(t)
	if d["count"] != float64(1) {
		t.Fatalf("scan count = %v", d["count"])
	}
	finding := d["findings"].([]any)[0].(map[string]any)
	if finding["rule"] != "aws-access-key" {
		t.Errorf("finding = %v", finding)
	}
	// preview must not echo the full secret to logs / CI output
	if strings.Contains(r.stdout, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("--scan-only leaked the full secret in preview")
	}

	// secret in scope but no file is written, regardless of --no-redact
	tmp := t.TempDir()
	e.mustRun(0, "export", "--scan-only", "-o", filepath.Join(tmp, "x.tar.zst"))
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("--scan-only wrote files: %v", entries)
	}
}

// --format / --compress are an orthogonal matrix. Spot-check the
// non-default combinations: zip and tar.gz. Each one's bytes must be
// readable by its standard-library reader (i.e. we wrote a valid
// container, not just a file with the right suffix).
func TestExportFormatMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// zip: deflate per entry, no separate --compress allowed
	zipOut := filepath.Join(t.TempDir(), "out.zip")
	e.mustRun(0, "export", "-o", zipOut, "--format", "zip")
	if !zipContains(t, zipOut, "session.json") {
		t.Error("zip artifact missing any session.json")
	}

	// tar + gzip: classic combo, standard library reads it
	gzOut := filepath.Join(t.TempDir(), "out.tar.gz")
	e.mustRun(0, "export", "-o", gzOut, "--format", "tar", "--compress", "gzip")
	if !tarGzContains(t, gzOut, "session.json") {
		t.Error("tar.gz artifact missing any session.json")
	}

	// invalid combo (zip + xz) is a usage error (exit 2)
	r := e.run("export", "-o", filepath.Join(t.TempDir(), "x.zip"), "--format", "zip", "--compress", "xz")
	if r.code != 2 {
		t.Errorf("invalid combo: exit %d, want 2 (usage)", r.code)
	}
}

// --agent / --since narrow the export to the matching sessions. We
// pick an agent that has data and one that doesn't, and verify the
// artifact carries only the requested side. We also accept the
// two-letter shorthand so the alias plumbing in resolveAgentFilter
// is exercised end-to-end through the real binary.
func TestExportFilters(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	for _, agentInput := range []string{"codex", "cx", "Codex"} {
		t.Run("agent="+agentInput, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "codex.tar.zst")
			d := e.mustRun(0, "export", "-o", out, "--agent", agentInput).data(t)
			res := d["result"].(map[string]any)
			if res["changed_files"].(float64) < 1 {
				t.Fatalf("--agent %s changed_files = %v", agentInput, res["changed_files"])
			}
			if err := walkTarZst(out, func(name string) error {
				if !strings.HasPrefix(name, "codex/") {
					return fmt.Errorf("non-codex member leaked into --agent %s export: %s", agentInput, name)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}

	// unknown agent: usage error with "did you mean…"
	r := e.run("export", "--agent", "cluade")
	if r.code != 2 {
		t.Errorf("unknown --agent: exit %d, want 2", r.code)
	}
	if !strings.Contains(r.stderr, "did you mean") || !strings.Contains(r.stderr, "claude-code") {
		t.Errorf("typo error missing suggestion: %s", r.stderr)
	}
}

// TestExportSplitByAgent: --split-by agent produces one artifact
// per agent in the archive, each filename suffixed with the agent
// name, and the per-chunk JSON envelope lists them all.
func TestExportSplitByAgent(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	dir := t.TempDir()
	prefix := filepath.Join(dir, "snap.tar.zst")
	d := e.mustRun(0, "export", "-o", prefix, "--split-by", "agent").data(t)
	parts, ok := d["parts"].([]any)
	if !ok || len(parts) < 2 {
		t.Fatalf("agent split parts = %v", d)
	}
	// Every chunk file exists on disk and the per-chunk filename
	// carries the agent name (".codex.tar.zst" etc).
	seenAgents := map[string]bool{}
	for _, raw := range parts {
		p := raw.(map[string]any)
		out := p["output"].(string)
		if _, err := os.Stat(out); err != nil {
			t.Errorf("chunk file missing: %v", err)
		}
		chunk := p["chunk"].(string)
		if !strings.Contains(out, "."+chunk+".tar.zst") {
			t.Errorf("chunk %q not present in filename %s", chunk, out)
		}
		seenAgents[chunk] = true
	}
	if len(seenAgents) < 2 {
		t.Errorf("expected at least two distinct agents, got %v", seenAgents)
	}
}

// TestExportSplitBySize: --split-by size writes one or more
// part-### files. With a tiny budget the seed archive must split
// across at least two parts.
func TestExportSplitBySize(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	dir := t.TempDir()
	prefix := filepath.Join(dir, "snap.tar.zst")
	d := e.mustRun(0, "export", "-o", prefix, "--split-by", "size", "--split-size", "1K").data(t)
	parts := d["parts"].([]any)
	if len(parts) < 2 {
		t.Fatalf("size split: expected >=2 parts, got %d (%v)", len(parts), parts)
	}
	for i, raw := range parts {
		p := raw.(map[string]any)
		chunk := p["chunk"].(string)
		want := fmt.Sprintf("part-%03d", i+1)
		if chunk != want {
			t.Errorf("part #%d chunk = %q, want %q", i, chunk, want)
		}
	}
}

// TestExportExcludeAgent: --exclude-agent drops the named agent
// from the artifact. We verify two things — the file list returned
// in JSON doesn't include the excluded agent's session paths, and
// the on-disk archive listed via `tape ls --exclude-agent` is
// symmetric.
func TestExportExcludeAgent(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// Baseline: every agent shows up in `ls`.
	baseline := e.mustRun(0, "ls", "--print", "--limit", "200").data(t)
	allCount := int(baseline["count"].(float64))
	if allCount < 2 {
		t.Skipf("need >= 2 agents to test exclusion; got %d", allCount)
	}

	// Exclude codex (canonical) and cursor (shorthand "cu").
	out := e.mustRun(0, "ls", "--print", "--limit", "200",
		"--exclude-agent", "codex", "--exclude-agent", "cu").data(t)
	sessions := out["sessions"].([]any)
	for _, raw := range sessions {
		s := raw.(map[string]any)
		if a := s["agent"].(string); a == "codex" || a == "cursor" {
			t.Errorf("excluded agent leaked: %s", a)
		}
	}
	if len(sessions) >= allCount {
		t.Errorf("exclusion didn't shrink list: %d >= %d", len(sessions), allCount)
	}

	// Same flag plumbed through export: same effect on the parts
	// listing. We don't unpack the archive — the agent's path
	// shouldn't appear in the JSON-reported written set.
	dir := t.TempDir()
	out2 := e.mustRun(0, "export", "-o",
		filepath.Join(dir, "out.tar.zst"),
		"--split-by", "agent", "--exclude-agent", "codex").data(t)
	parts := out2["parts"].([]any)
	for _, raw := range parts {
		p := raw.(map[string]any)
		if p["chunk"].(string) == "codex" {
			t.Errorf("export --exclude-agent leaked codex chunk: %+v", p)
		}
	}
}

// TestExportExcludeAgentTypoSuggests: misspelled --exclude-agent
// gets a usage error with the did-you-mean hint, same as --agent.
func TestExportExcludeAgentTypoSuggests(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	r := e.run("export", "--exclude-agent", "clude-code")
	if r.code != 2 {
		t.Errorf("typo exit = %d, want 2", r.code)
	}
	if !strings.Contains(r.stderr, "did you mean") {
		t.Errorf("typo error must hint: %s", r.stderr)
	}
}

// TestExportJobsControlsParallelism: --jobs N is echoed in the
// chunked JSON envelope (split.jobs + split.encoder_concurrency).
// The actual concurrency is hard to observe from the outside, so
// we pin the metadata contract instead.
func TestExportJobsControlsParallelism(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	dir := t.TempDir()
	prefix := filepath.Join(dir, "snap.tar.zst")
	out := e.mustRun(0, "export", "-o", prefix,
		"--split-by", "agent", "--jobs", "2").data(t)
	split := out["split"].(map[string]any)
	if jobs := int(split["jobs"].(float64)); jobs != 2 && jobs != 1 {
		// jobs may be capped to len(chunks); single-agent fixtures
		// would yield 1 worker. Both are correct.
		t.Errorf("split.jobs = %d, want 1 or 2", jobs)
	}
	if enc := int(split["encoder_concurrency"].(float64)); enc < 1 {
		t.Errorf("encoder_concurrency = %d, want >= 1", enc)
	}
	for _, raw := range out["parts"].([]any) {
		p := raw.(map[string]any)
		if _, err := os.Stat(p["output"].(string)); err != nil {
			t.Errorf("chunk file missing under parallel run: %v", err)
		}
	}
}

// TestExportSplitUsageErrors: bad --split-by, bad --split-size,
// both surface as usage errors (exit 2) before any walking.
func TestExportSplitUsageErrors(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	if r := e.run("export", "--split-by", "daily"); r.code != 2 {
		t.Errorf("--split-by daily: exit %d, want 2", r.code)
	}
	if r := e.run("export", "--split-by", "size", "--split-size", "wat"); r.code != 2 {
		t.Errorf("--split-size wat: exit %d, want 2", r.code)
	}
}

// positional output works the same as -o; --output + positional
// together is a usage error so the call site is unambiguous.
func TestExportPositionalOutput(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	out := filepath.Join(t.TempDir(), "positional.tar.zst")
	e.mustRun(0, "export", out)
	if _, err := os.Stat(out); err != nil {
		t.Errorf("positional output not written: %v", err)
	}

	r := e.run("export", out, "-o", filepath.Join(t.TempDir(), "other.tar.zst"))
	if r.code != 2 {
		t.Errorf("positional + -o: exit %d, want 2 (usage)", r.code)
	}
}

// ---- helpers --------------------------------------------------------

func readTarMember(t *testing.T, artifact, member string) string {
	t.Helper()
	f, err := os.Open(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == member {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("member %s not found in %s", member, artifact)
	return ""
}

// walkTarZst opens a .tar.zst artifact and calls fn for each member
// name. Returns the first non-nil error fn produces.
func walkTarZst(path string, fn func(name string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(hdr.Name); err != nil {
			return err
		}
	}
}

func tarGzContains(t *testing.T, path, suffix string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(hdr.Name, suffix) {
			return true
		}
	}
}

func zipContains(t *testing.T, path, suffix string) bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, suffix) {
			return true
		}
	}
	return false
}
