package e2e

import (
	"archive/tar"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// The secret gate: a clean archive scans clean and pushes; a planted secret
// blocks the push until explicitly allowed.
func TestBackupSecretGate(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	requireGit(t)
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	// clean archive: scan ok
	d := e.mustRun(0, "backup", "scan").data(t)
	if d["count"] != float64(0) {
		t.Fatalf("clean archive has findings: %v", d)
	}

	e.plantSecret()

	// scan reports the finding and exits 1 (machine-checkable gate)
	r := e.mustRun(1, "backup", "scan")
	d = r.data(t)
	if d["count"] != float64(1) {
		t.Fatalf("scan count = %v", d["count"])
	}
	finding := d["findings"].([]any)[0].(map[string]any)
	if finding["rule"] != "aws-access-key" {
		t.Errorf("finding = %v", finding)
	}
	// the preview must not echo the full secret
	if strings.Contains(r.stdout, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("scan output leaks the full secret")
	}

	// push is blocked
	r = e.mustRun(1, "backup", "push")
	if r.errJSON(t)["error"] != "secrets_found" {
		t.Errorf("push error = %v", r.errJSON(t))
	}

	// explicit override works
	e.mustRun(0, "backup", "push", "--allow-secrets")
}

// Full disaster-recovery drill: push to a bare remote, then restore the
// archive on a "new machine" (fresh TAPE_DIR) and search it.
func TestBackupGitRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	requireGit(t)
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")

	bare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v %s", err, out)
	}

	// dry run first: exit 10, nothing pushed
	e.mustRun(10, "backup", "push", "--dry-run")

	d := e.mustRun(0, "backup", "push", "--remote", bare).data(t)
	res := d["result"].(map[string]any)
	if res["note"] != "pushed to origin" {
		t.Fatalf("push result: %v", res)
	}

	// new machine: same HOME (for agent binaries), fresh TAPE_DIR
	e2 := &env{t: t, home: e.home, dir: t.TempDir()}
	d = e2.mustRun(0, "backup", "pull", "--remote", bare).data(t)
	res = d["result"].(map[string]any)
	if res["action"] != "clone" || !strings.Contains(res["note"].(string), "reindexed 3") {
		t.Fatalf("pull result: %v", res)
	}

	// the restored archive is immediately searchable
	d = e2.mustRun(0, "search", "构建速度").data(t)
	if d["count"] != float64(1) {
		t.Errorf("search on restored machine: %v", d["count"])
	}
}

// Export redacts secrets inside the artifact while leaving local files intact.
func TestBackupExportRedaction(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedAllAgents()
	e.mustRun(0, "sync")
	e.plantSecret()

	out := filepath.Join(t.TempDir(), "archive.tar.zst")
	e.mustRun(10, "backup", "export", "--output", out, "--dry-run")
	if _, err := os.Stat(out); err == nil {
		t.Fatal("dry run wrote the artifact")
	}
	e.mustRun(0, "backup", "export", "--output", out)

	planted := readTarMember(t, out, "codex/planted/deadbeef/session.json")
	if strings.Contains(planted, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("artifact leaks the secret")
	}
	if !strings.Contains(planted, "[REDACTED:aws-access-key]") {
		t.Errorf("redaction marker missing: %s", planted)
	}

	// local archive must be untouched
	local, _ := os.ReadFile(filepath.Join(e.dir, "archive", "codex", "planted", "deadbeef", "session.json"))
	if !strings.Contains(string(local), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("export modified the local archive")
	}

	// --no-redact keeps the secret verbatim (explicit opt-out)
	rawOut := filepath.Join(t.TempDir(), "raw.tar.zst")
	e.mustRun(0, "backup", "export", "--output", rawOut, "--no-redact")
	if !strings.Contains(readTarMember(t, rawOut, "codex/planted/deadbeef/session.json"), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("--no-redact did not keep the secret")
	}
}

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
