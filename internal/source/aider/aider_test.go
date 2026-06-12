package aider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// Aider's real on-disk format, captured verbatim from the published docs
// and the splitter in aider/utils.py.
const sampleHistory = `# aider chat started at 2026-06-12 10:30:45

#### Fix the failing tests in api_test.go

> Added api_test.go to the chat.
> Running pytest...

Looking at api_test.go, the failure is in TestSignup. Let me read it.

#### make it pass

I'll change line 42 to use the new signature.

# aider chat started at 2026-06-12 11:05:00

#### Add a CLI flag --verbose

OK, I'll add the flag and wire it through the logger.
`

func writeHistory(t *testing.T, home string) string {
	t.Helper()
	p := filepath.Join(home, ".aider.chat.history.md")
	if err := os.WriteFile(p, []byte(sampleHistory), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectAndListSplitsSessions(t *testing.T) {
	home := t.TempDir()
	src := New(home)

	if found, _, _ := src.Detect(context.Background()); found {
		t.Fatal("Detect on empty home should be false")
	}
	writeHistory(t, home)
	if found, _, _ := src.Detect(context.Background()); !found {
		t.Fatal("Detect after seed should be true")
	}
	refs, err := src.List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Two `# aider chat started at` headers → two sessions.
	if len(refs) != 2 {
		t.Fatalf("got %d sessions, want 2", len(refs))
	}
	// Ids must be stable across calls.
	refs2, _ := src.List(context.Background(), time.Time{})
	if refs[0].SourceID != refs2[0].SourceID {
		t.Errorf("session id unstable across calls: %s vs %s",
			refs[0].SourceID, refs2[0].SourceID)
	}
}

func TestLoadClassifiesRoles(t *testing.T) {
	home := t.TempDir()
	writeHistory(t, home)
	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})

	// First session: 2 user messages + 1 tool block + 2 assistant runs.
	s1, err := src.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if s1 == nil {
		t.Fatal("first session is nil")
	}
	if s1.Title != "Fix the failing tests in api_test.go" {
		t.Errorf("title: %q", s1.Title)
	}
	if !s1.StartedAt.Equal(time.Date(2026, 6, 12, 10, 30, 45, 0, time.UTC)) {
		t.Errorf("startedAt: %v", s1.StartedAt)
	}

	wantRoles := []model.Role{model.RoleUser, model.RoleTool, model.RoleAssistant, model.RoleUser, model.RoleAssistant}
	if len(s1.Messages) != len(wantRoles) {
		t.Fatalf("first session msgs: got %d want %d", len(s1.Messages), len(wantRoles))
	}
	for i, want := range wantRoles {
		if s1.Messages[i].Role != want {
			t.Errorf("msg[%d] role = %v want %v (text=%q)",
				i, s1.Messages[i].Role, want, s1.Messages[i].Text)
		}
	}
	// Second session must have its own messages, not leak from first.
	s2, _ := src.Load(context.Background(), refs[1])
	if s2 == nil || len(s2.Messages) != 2 {
		t.Fatalf("second session: %+v", s2)
	}
	if !strings.HasPrefix(s2.Messages[0].Text, "Add a CLI flag") {
		t.Errorf("second session text leak: %q", s2.Messages[0].Text)
	}
}

func TestLoadHandlesUnheaderedFile(t *testing.T) {
	home := t.TempDir()
	// Legacy aider files might lack the `# aider chat started at ...`
	// header (e.g. when a user appends manually). We should still treat
	// the whole file as a single session instead of returning nothing.
	p := filepath.Join(home, ".aider.chat.history.md")
	_ = os.WriteFile(p, []byte("#### hello\n\nhi there\n"), 0o600)
	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})
	if len(refs) != 1 {
		t.Fatalf("unheadered file: got %d sessions, want 1", len(refs))
	}
	s, err := src.Load(context.Background(), refs[0])
	if err != nil || s == nil {
		t.Fatalf("Load unheadered: %v %v", err, s)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("unheadered messages: %d", len(s.Messages))
	}
}
