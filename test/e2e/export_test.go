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
