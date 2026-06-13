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
	// One entry per source per host. The exact count is "however many
	// adapters we register" — we don't assert on the absolute number to
	// avoid breaking every time Phase B/C/D lands a new agent.
	if hosts[""] == 0 || hosts["dev@build-server"] == 0 ||
		hosts[""] != hosts["dev@build-server"] {
		t.Errorf("source hosts (expected symmetric local/remote): %v", hosts)
	}

	// remote session is searchable like any other
	d = e.mustRun(0, "search", "构建速度").data(t)
	if d["count"].(float64) < 1 {
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

	// sync --full re-archives every session even when nothing has
	// changed, which is the whole point: parser-upgrade backfills.
	// Without --remote it only re-walks local sources; with --remote
	// it covers the mirror too. We assert both shapes so a regression
	// in either path is caught.
	d = e.mustRun(0, "sync", "--full").data(t)
	if d["archived"] != float64(1) {
		t.Errorf("--full local-only sync archived = %v, want 1", d["archived"])
	}
	d = e.mustRun(0, "sync", "--full", "--remote", "dev@build-server").data(t)
	if d["archived"] != float64(2) {
		t.Errorf("--full --remote sync archived = %v, want 2", d["archived"])
	}

	// Summary.Host on ls JSON lets script consumers split local /
	// remote without a second show call; the codex row must carry
	// the host, the claude row must not.
	d = e.mustRun(0, "ls").data(t)
	sums := d["sessions"].([]any)
	saw := map[string]string{}
	for _, raw := range sums {
		s := raw.(map[string]any)
		host, _ := s["host"].(string)
		saw[s["agent"].(string)] = host
	}
	if saw["codex"] != "dev@build-server" {
		t.Errorf("codex Summary.Host = %q, want dev@build-server", saw["codex"])
	}
	if saw["claude-code"] != "" {
		t.Errorf("claude-code Summary.Host = %q, want empty", saw["claude-code"])
	}

	// --host filter narrows the list to one side of the split.
	d = e.mustRun(0, "ls", "--host", "local").data(t)
	for _, raw := range d["sessions"].([]any) {
		if h, _ := raw.(map[string]any)["host"].(string); h != "" {
			t.Errorf(`--host local leaked a remote row: host=%q`, h)
		}
	}
	d = e.mustRun(0, "ls", "--host", "dev@build-server").data(t)
	for _, raw := range d["sessions"].([]any) {
		if h, _ := raw.(map[string]any)["host"].(string); h != "dev@build-server" {
			t.Errorf(`--host dev@build-server leaked a non-matching row: host=%q`, h)
		}
	}

	// search --host scopes hits, and Hit.Host comes through so the
	// picker can label remote rows / route Resume through SSH.
	d = e.mustRun(0, "search", "--host", "dev@build-server", "构建速度").data(t)
	hits := d["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("remote-host search returned no hits")
	}
	for _, raw := range hits {
		h := raw.(map[string]any)
		if h["host"] != "dev@build-server" {
			t.Errorf(`Hit.Host = %v, want dev@build-server`, h["host"])
		}
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
