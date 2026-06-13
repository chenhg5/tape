package cli

import (
	"encoding/base64"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestAgentResumeArgs(t *testing.T) {
	cases := []struct {
		agent string
		want  []string // nil = no native resume args
	}{
		{"claude-code", []string{"--resume", "uuid-123"}},
		{"codex", []string{"resume", "uuid-123"}},
		{"antigravity", []string{"--conversation", "uuid-123"}},
		{"qoder", []string{"-r", "uuid-123"}},
		{"mimocode", []string{"--session", "uuid-123"}},
		{"kimi-code", []string{"--session", "uuid-123"}},
		{"cursor", nil},
		{"gemini", nil},
		{"qwen", nil},
		{"iflow", nil},
		{"opencode", nil},
		{"aider", nil},
		{"unknown-agent", nil},
	}
	for _, c := range cases {
		got := agentResumeArgs(c.agent, "uuid-123")
		if !slicesEqual(got, c.want) {
			t.Errorf("agentResumeArgs(%q) = %v, want %v", c.agent, got, c.want)
		}
	}

	// Sanity: empty sourceID must never produce args — would launch
	// the agent with a bogus "--resume" / no arg and likely fail.
	if got := agentResumeArgs("claude-code", ""); got != nil {
		t.Errorf("agentResumeArgs with empty sourceID = %v, want nil", got)
	}
}

func TestResumeCmdline(t *testing.T) {
	// Local path: just `<bin> [resume-args]`. No SSH, no cd.
	local := []struct {
		agent, sourceID, want string
	}{
		{"claude-code", "abc", "claude --resume abc"},
		{"codex", "abc", "codex resume abc"},
		{"antigravity", "abc", "agy --conversation abc"},
		{"qoder", "abc", "qodercli -r abc"},
		{"mimocode", "abc", "mimo --session abc"},
		{"kimi-code", "abc", "kimi --session abc"},
		{"cursor", "abc", "cursor-agent"}, // no native resume → bare launch
		{"aider", "abc", "aider"},
	}
	for _, c := range local {
		s := &model.Session{Agent: c.agent, SourceID: c.sourceID}
		if got := resumeCmdline(s); got != c.want {
			t.Errorf("local resumeCmdline(%s/%s) = %q, want %q",
				c.agent, c.sourceID, got, c.want)
		}
	}

	// Remote path: ssh <host> -t '<cd && cmd>'. cd is omitted when
	// CWD is empty so resume still works on stale archives where the
	// directory is unknown. Quoting uses POSIX single quotes so a cwd
	// with spaces or odd characters survives the remote shell parse.
	remote := []struct {
		name, agent, sid, cwd, host, want string
	}{
		{"with-cwd", "claude-code", "abc", "/root/x", "user@build-01",
			`ssh user@build-01 -t 'cd '\''/root/x'\'' && claude --resume abc'`},
		{"no-cwd", "codex", "s-1", "", "h",
			`ssh h -t 'codex resume s-1'`},
		{"cwd-with-space", "codex", "s-1", "/home/me/with space", "h",
			`ssh h -t 'cd '\''/home/me/with space'\'' && codex resume s-1'`},
		{"agent-without-resume-flag", "cursor", "abc", "/p", "h",
			`ssh h -t 'cd '\''/p'\'' && cursor-agent'`},
	}
	for _, c := range remote {
		t.Run(c.name, func(t *testing.T) {
			s := &model.Session{
				Agent: c.agent, SourceID: c.sid, CWD: c.cwd,
				Meta: map[string]string{"host": c.host},
			}
			if got := resumeCmdline(s); got != c.want {
				t.Errorf("remote resumeCmdline:\n got  %s\n want %s", got, c.want)
			}
		})
	}
}

func TestAgentLaunchHintDistinguishesNativeResume(t *testing.T) {
	// Agents with a CLI resume flag get a "resumed inside this
	// conversation" hint; those without fall back to the built-in
	// /resume picker phrasing.
	resumeAgents := []string{"claude-code", "codex", "antigravity", "qoder", "mimocode", "kimi-code"}
	for _, a := range resumeAgents {
		h := agentLaunchHint(a, "abc")
		if !strings.Contains(h, "resumed inside this conversation") {
			t.Errorf("%s hint missing native-resume wording: %q", a, h)
		}
	}
	pickerAgents := []string{"cursor", "gemini", "qwen", "iflow", "aider", "opencode"}
	for _, a := range pickerAgents {
		h := agentLaunchHint(a, "abc")
		if !strings.Contains(h, "/resume") {
			t.Errorf("%s hint should mention built-in /resume: %q", a, h)
		}
	}
}

// TestEmitOSC52Encoding pins the wire format of the OSC 52 escape we
// emit. Bugs here are silent because most terminals just drop the
// sequence instead of raising — so we lock it down in a test.
func TestEmitOSC52Encoding(t *testing.T) {
	got := captureStderr(t, func() { emitOSC52("tape/abc-123") })
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("tape/abc-123")) + "\x07"
	if got != want {
		t.Errorf("OSC 52 output = %q, want %q", got, want)
	}
}

// TestCopyClipboardFallsBackToOSC52 verifies the last-resort path
// fires when no $TMUX and no helper binaries are reachable on $PATH.
// We blank $PATH (and force-clear $TMUX) for the duration of the test
// so the strategy stack drops all the way through.
func TestCopyClipboardFallsBackToOSC52(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("PATH", "")

	var method string
	got := captureStderr(t, func() { method = copyClipboard("payload-xyz") })

	wantEsc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("payload-xyz")) + "\x07"
	if got != wantEsc {
		t.Errorf("OSC 52 fallback bytes = %q, want %q", got, wantEsc)
	}
	if !strings.HasPrefix(method, "OSC 52") {
		t.Errorf("method label = %q, want it to start with %q", method, "OSC 52")
	}
}

// TestClipboardHelpersIncludeReasonableDefaults sanity-checks the
// per-OS helper list so a future refactor doesn't accidentally leave
// e.g. Linux with no candidate at all.
func TestClipboardHelpersIncludeReasonableDefaults(t *testing.T) {
	hs := clipboardHelpers()
	if len(hs) == 0 {
		t.Fatalf("no clipboard helpers for GOOS=%s", runtime.GOOS)
	}
	bins := make([]string, len(hs))
	for i, h := range hs {
		bins[i] = h.bin
	}
	want := map[string][]string{
		"darwin":  {"pbcopy"},
		"linux":   {"wl-copy", "xclip", "xsel", "clip.exe"},
		"windows": {"clip"},
	}
	if expected, ok := want[runtime.GOOS]; ok {
		for _, w := range expected {
			found := false
			for _, b := range bins {
				if b == w {
					found = true
				}
			}
			if !found {
				t.Errorf("clipboardHelpers for %s missing %q (got %v)", runtime.GOOS, w, bins)
			}
		}
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
