package cli

import (
	"encoding/base64"
	"io"
	"os"
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
	cases := []struct {
		agent, sourceID, want string
	}{
		{"claude-code", "abc", "claude --resume abc"},
		{"codex", "abc", "codex resume abc"},
		{"antigravity", "abc", "agy --conversation abc"},
		{"qoder", "abc", "qodercli -r abc"},
		{"cursor", "abc", "cursor-agent"}, // no native resume → bare launch
		{"aider", "abc", "aider"},
	}
	for _, c := range cases {
		s := &model.Session{Agent: c.agent, SourceID: c.sourceID}
		if got := resumeCmdline(s); got != c.want {
			t.Errorf("resumeCmdline(%s/%s) = %q, want %q",
				c.agent, c.sourceID, got, c.want)
		}
	}
}

func TestAgentLaunchHintDistinguishesNativeResume(t *testing.T) {
	// Agents with a CLI resume flag get a "resumed inside this
	// conversation" hint; those without fall back to the built-in
	// /resume picker phrasing.
	resumeAgents := []string{"claude-code", "codex", "antigravity", "qoder"}
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

// TestCopyOSC52Encoding pins the wire format of the OSC 52 escape we
// emit. Bugs here are silent because most terminals just drop the
// sequence instead of raising — so we lock it down in a test.
func TestCopyOSC52Encoding(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	const payload = "tape/abc-123"
	copyOSC52(payload)
	w.Close()

	got, _ := io.ReadAll(r)
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(payload)) + "\x07"
	if string(got) != want {
		t.Errorf("OSC 52 output = %q, want %q", got, want)
	}
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
