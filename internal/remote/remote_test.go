package remote

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// stubSSH installs a fake `ssh` on PATH that ignores the host argument and
// executes the remote script locally with HOME pointed at remoteHome —
// exactly what a real remote shell would do.
func stubSSH(t *testing.T, remoteHome string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
# drop ssh options (-o val) and the host, keep the command
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
shift # host
export HOME="` + remoteHome + `"
exec sh -c "$@"`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func seedRemoteHome(t *testing.T, home string) {
	t.Helper()
	files := map[string]string{
		".claude/projects/-root-p/s1.jsonl":          `{"type":"user"}`,
		".codex/sessions/2026/06/01/rollout-x.jsonl": `{"type":"session_meta"}`,
	}
	for name, content := range files {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPullMirrorsRemoteHome(t *testing.T) {
	remoteHome := t.TempDir()
	seedRemoteHome(t, remoteHome)
	stubSSH(t, remoteHome)

	m := &Mirror{Host: "user@build-server", Dir: t.TempDir()}
	if err := m.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.Dir, ".claude/projects/-root-p/s1.jsonl"))
	if err != nil || string(data) != `{"type":"user"}` {
		t.Errorf("mirrored claude file: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, ".codex/sessions/2026/06/01/rollout-x.jsonl")); err != nil {
		t.Errorf("mirrored codex file: %v", err)
	}
	// state file written for incremental pulls
	if _, err := os.Stat(m.statePath()); err != nil {
		t.Errorf("state file: %v", err)
	}
}

func TestPullIncremental(t *testing.T) {
	remoteHome := t.TempDir()
	seedRemoteHome(t, remoteHome)
	stubSSH(t, remoteHome)

	m := &Mirror{Host: "h", Dir: t.TempDir()}
	if err := m.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	// a new file appears on the remote
	p := filepath.Join(remoteHome, ".claude/projects/-root-p/s2.jsonl")
	if err := os.WriteFile(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, ".claude/projects/-root-p/s2.jsonl")); err != nil {
		t.Errorf("incremental pull missed new file: %v", err)
	}
}

func TestPullEmptyRemote(t *testing.T) {
	stubSSH(t, t.TempDir()) // remote has no agent dirs at all
	m := &Mirror{Host: "h", Dir: t.TempDir()}
	if err := m.Pull(context.Background()); err != nil {
		t.Errorf("empty remote must not error: %v", err)
	}
}

func TestPullSSHFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 255\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	m := &Mirror{Host: "h", Dir: t.TempDir()}
	err := m.Pull(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("ssh failure must surface stderr, got %v", err)
	}
	if _, statErr := os.Stat(m.statePath()); statErr == nil {
		t.Error("failed pull must not advance the sync state")
	}
}

func TestExtractBlocksTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	payload := []byte("pwned")
	tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o600, Size: int64(len(payload))})
	tw.Write(payload)
	tw.Close()

	root := filepath.Join(t.TempDir(), "mirror")
	os.MkdirAll(root, 0o700)
	if err := extract(&buf, root); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("traversal not blocked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Error("file escaped the mirror dir")
	}
}

func TestRemoteScript(t *testing.T) {
	s := remoteScript("")
	for _, d := range AgentDirs {
		if !strings.Contains(s, d) {
			t.Errorf("script missing dir %s", d)
		}
	}
	if strings.Contains(s, "--newer-mtime") {
		t.Error("full pull must not pass --newer-mtime")
	}
	s = remoteScript("2026-06-12 10:00:00 UTC")
	if !strings.Contains(s, `--newer-mtime "2026-06-12 10:00:00 UTC"`) {
		t.Errorf("incremental flag missing: %s", s)
	}
}

func TestSanitizeHost(t *testing.T) {
	cases := map[string]string{
		"user@build-01":  "user-build-01",
		"10.0.0.7":       "10.0.0.7",
		"u@h:2222":       "u-h-2222",
		"weird$(rm)/../": "weird--rm--..-", // no path separators survive
	}
	for in, want := range cases {
		if got := sanitizeHost(in); got != want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

type loadOnlySource struct{ sess *model.Session }

func (l *loadOnlySource) Name() string { return "codex" }
func (l *loadOnlySource) Detect(context.Context) (bool, string, error) {
	return true, "", nil
}
func (l *loadOnlySource) List(context.Context, time.Time) ([]ports.SessionRef, error) {
	return nil, nil
}
func (l *loadOnlySource) Load(context.Context, ports.SessionRef) (*model.Session, error) {
	return l.sess, nil
}

func TestWrapSourceAddsHostMeta(t *testing.T) {
	inner := &loadOnlySource{sess: &model.Session{ID: "codex/x"}}
	wrapped := WrapSource(inner, "user@build-01")

	s, err := wrapped.Load(context.Background(), ports.SessionRef{})
	if err != nil || s.Meta["host"] != "user@build-01" {
		t.Errorf("host meta: %v err=%v", s.Meta, err)
	}
	if h, ok := wrapped.(interface{ Host() string }); !ok || h.Host() != "user@build-01" {
		t.Error("wrapped source must expose Host()")
	}
	if wrapped.Name() != "codex" {
		t.Errorf("name must pass through, got %q", wrapped.Name())
	}

	// nil session (empty) passes through untouched
	inner.sess = nil
	if s, err := wrapped.Load(context.Background(), ports.SessionRef{}); s != nil || err != nil {
		t.Errorf("nil session: %v %v", s, err)
	}
}
