package cli

import (
	"bytes"
	"strings"
	"testing"
)

// newTestProgress hand-wires a Progress that always renders, into a buffer,
// so we can assert what the user would see on a TTY without one being open.
func newTestProgress(label string, total int64) (*Progress, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	p := &Progress{w: buf, enabled: true, color: false, label: label, total: total}
	return p, buf
}

func TestProgressBarRenders(t *testing.T) {
	p, buf := newTestProgress("syncing", 10)
	p.Update(3, "claude-code/abc")
	p.Done("synced 3")

	out := buf.String()
	if !strings.Contains(out, "syncing") {
		t.Fatalf("expected label, got %q", out)
	}
	if !strings.Contains(out, "3/10") {
		t.Fatalf("expected counter, got %q", out)
	}
	if !strings.Contains(out, "█") {
		t.Fatalf("expected filled glyph, got %q", out)
	}
	if !strings.Contains(out, "synced 3") {
		t.Fatalf("expected final message, got %q", out)
	}
}

func TestProgressSpinnerWhenTotalUnknown(t *testing.T) {
	p, buf := newTestProgress("syncing", 0)
	p.Update(1, "first") // first redraw is rate-limited away
	p.draw(true)         // force one frame so the test is stable
	p.Done("")

	out := buf.String()
	hasFrame := false
	for _, r := range []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		if strings.ContainsRune(out, r) {
			hasFrame = true
			break
		}
	}
	if !hasFrame {
		t.Fatalf("expected a spinner frame in output: %q", out)
	}
	if !strings.Contains(out, "syncing") {
		t.Fatalf("expected label, got %q", out)
	}
}

func TestProgressDisabledIsSilent(t *testing.T) {
	buf := &bytes.Buffer{}
	p := &Progress{w: buf, enabled: false, label: "x", total: 1}
	p.Update(1, "note")
	p.Done("final")

	if got := buf.String(); got != "final\n" {
		t.Fatalf("disabled progress should only print Done message, got %q", got)
	}
}

func TestProgressUpdateClipsAt100Percent(t *testing.T) {
	p, buf := newTestProgress("export", 4)
	p.Update(10, "over") // simulate over-shoot; should still cap visually
	p.Done("")
	out := buf.String()
	if !strings.Contains(out, "10/4") {
		t.Fatalf("expected raw counter, got %q", out)
	}
	if strings.Count(out, "░") > 0 {
		t.Fatalf("at >=100%% the bar should be fully filled, got %q", out)
	}
}

func TestRepeatHelper(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"a", 3, "aaa"},
		{"ab", 0, ""},
		{"x", -1, ""},
		{"█", 2, "██"},
	}
	for _, tc := range cases {
		if got := repeat(tc.in, tc.n); got != tc.want {
			t.Errorf("repeat(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestColorizeOffPassthrough(t *testing.T) {
	if got := colorize(false, "31", "hi"); got != "hi" {
		t.Fatalf("expected raw text when color is off, got %q", got)
	}
	if got := colorize(true, "31", "hi"); !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("expected ANSI code when color is on, got %q", got)
	}
}
