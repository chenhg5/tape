package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSSH puts a fake `ssh` on the tape subprocess's PATH. It ignores the
// host and runs the remote script locally with HOME set to remoteHome,
// which is exactly what the real ssh would do on the far end.
func (e *env) stubSSH(remoteHome string) {
	e.t.Helper()
	dir := e.t.TempDir()
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
shift
export HOME="` + remoteHome + `"
exec sh -c "$@"`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		e.t.Fatal(err)
	}
	e.pathPrep = dir
}

// A second machine's sessions are synced over SSH, searchable next to
// local ones, and tagged with their origin host.
func TestRemoteSync(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	e.seedClaude() // local machine has one claude session

	// the "remote machine" has a codex session
	remoteHome := t.TempDir()
	re := &env{t: t, home: remoteHome, dir: t.TempDir()}
	re.seedCodex()
	e.stubSSH(remoteHome)

	d := e.mustRun(0, "sync", "--remote", "dev@build-server").data(t)
	if d["archived"] != float64(2) { // 1 local claude + 1 remote codex
		t.Fatalf("archived = %v, want 2", d["archived"])
	}
	// the report distinguishes local from remote sources
	hosts := map[string]int{}
	for _, s := range d["sources"].([]any) {
		src := s.(map[string]any)
		host, _ := src["host"].(string)
		hosts[host]++
	}
	if hosts[""] != 3 || hosts["dev@build-server"] != 3 {
		t.Errorf("source hosts: %v", hosts)
	}

	// remote session is searchable like any other
	d = e.mustRun(0, "search", "构建速度").data(t)
	if d["count"] != float64(1) {
		t.Fatalf("remote session not searchable: %v", d["count"])
	}

	// and carries its origin host in meta
	sess := e.mustRun(0, "show", codexSessionID[:8]).data(t)
	meta := sess["meta"].(map[string]any)
	if meta["host"] != "dev@build-server" {
		t.Errorf("session meta host = %v", meta["host"])
	}

	// re-sync is incremental: mirror state + archive checksums
	d = e.mustRun(0, "sync", "--remote", "dev@build-server").data(t)
	if d["archived"] != float64(0) {
		t.Errorf("re-sync archived = %v, want 0", d["archived"])
	}
}

func TestRemoteSyncSSHFailure(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ssh"),
		[]byte("#!/bin/sh\necho 'ssh: connect to host nope: Connection refused' >&2\nexit 255\n"), 0o755)
	e.pathPrep = dir

	r := e.run("sync", "--remote", "nope")
	if r.code != 1 {
		t.Errorf("ssh failure: exit %d, want 1", r.code)
	}
	if !strings.Contains(r.stderr, "Connection refused") {
		t.Errorf("stderr must carry the ssh error: %s", r.stderr)
	}
}
