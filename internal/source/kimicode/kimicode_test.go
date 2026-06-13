package kimicode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
)

// seedSession plants a kimi-code session under <home>/.kimi-code/
// (or the path resolveRoot returns for the given home). Returns the
// session ID and the full session directory so tests can poke at it.
func seedSession(t *testing.T, home, workDirKey, sessionID, stateJSON string, wire []string) string {
	t.Helper()
	root := filepath.Join(home, ".kimi-code")
	if h := os.Getenv("KIMI_CODE_HOME"); h != "" {
		root = h
	}
	sessDir := filepath.Join(root, "sessions", workDirKey, sessionID)
	mainDir := filepath.Join(sessDir, "agents", "main")
	if err := os.MkdirAll(mainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if stateJSON != "" {
		if err := os.WriteFile(filepath.Join(sessDir, "state.json"), []byte(stateJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mainDir, "wire.jsonl"), []byte(strings.Join(wire, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessDir
}

func event(eventType string, payload any) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "event",
		"params":  map[string]any{"type": eventType, "payload": payload},
	})
	return string(b)
}

func TestDetectFindsSessionsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")

	src := New(home)
	if found, _, _ := src.Detect(context.Background()); found {
		t.Error("Detect on empty home should be false")
	}

	seedSession(t, home, "wd_abc", "ses1", `{"title":"hi"}`, []string{
		event("TurnBegin", map[string]any{"user_input": "hello"}),
	})
	if found, root, _ := src.Detect(context.Background()); !found || root != filepath.Join(home, ".kimi-code") {
		t.Errorf("Detect: found=%v root=%s", found, root)
	}
}

func TestKimiCodeHomeEnvOverridesDefault(t *testing.T) {
	home := t.TempDir()
	override := t.TempDir()

	// Plant a session at the default ~/.kimi-code/ first, with no
	// env set, so we know the fixture writer doesn't get fooled.
	t.Setenv("KIMI_CODE_HOME", "")
	defaultDir := filepath.Join(home, ".kimi-code", "sessions", "wd_abc", "ses1", "agents", "main")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDir, "wire.jsonl"),
		[]byte(event("TurnBegin", map[string]any{"user_input": "x"})), 0o600); err != nil {
		t.Fatal(err)
	}

	// Now flip on KIMI_CODE_HOME and re-construct: Detect must look at
	// the override only, ignoring the default-rooted session above.
	t.Setenv("KIMI_CODE_HOME", override)
	src := New(home)
	if found, _, _ := src.Detect(context.Background()); found {
		t.Error("with KIMI_CODE_HOME set, default ~/.kimi-code/ must be ignored")
	}

	// And a session under the override IS picked up.
	seedSession(t, home, "wd_xyz", "ses2", `{"title":"override"}`, []string{
		event("TurnBegin", map[string]any{"user_input": "y"}),
	})
	if found, root, _ := src.Detect(context.Background()); !found || root != override {
		t.Errorf("override Detect: found=%v root=%s, want root=%s", found, root, override)
	}
}

func TestListPrefersIndexAndStillCoversFsWalk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")

	seedSession(t, home, "wd_a", "ses-index", `{"title":"in-index"}`, []string{
		event("TurnBegin", map[string]any{"user_input": "indexed"}),
	})
	seedSession(t, home, "wd_b", "ses-walk", `{"title":"only-fs"}`, []string{
		event("TurnBegin", map[string]any{"user_input": "walked"}),
	})
	// Index only mentions ses-index. The fs walk must still surface
	// ses-walk so we don't lose sessions written outside the index.
	indexPath := filepath.Join(home, ".kimi-code", "session_index.jsonl")
	if err := os.WriteFile(indexPath, []byte(
		`{"sessionId":"ses-index","sessionDir":"sessions/wd_a/ses-index","workDir":"/proj/a"}`+"\n"+
			`{"sessionId":"ses-bogus","sessionDir":"sessions/wd_a/missing","workDir":"/proj/a"}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	refs, err := New(home).List(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range refs {
		if r.Agent != "kimi-code" {
			t.Errorf("agent label = %q, want kimi-code", r.Agent)
		}
		ids[r.SourceID] = true
	}
	if !ids["ses-index"] || !ids["ses-walk"] {
		t.Fatalf("missing ids in %v", ids)
	}
	if ids["ses-bogus"] {
		t.Error("stale index entry pointing at a missing dir should not produce a ref")
	}
}

func TestLoadTurnsUserAssistantToolFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")
	seedSession(t, home, "wd_demo", "ses-demo",
		`{"title":"kimi flow","workDir":"/root/proj","model":"kimi-k2","createdAt":1781256000000}`,
		[]string{
			// Turn 1: user asks, assistant thinks + answers + runs a tool
			event("TurnBegin", map[string]any{"user_input": "重构这段代码"}),
			event("ContentPart", map[string]any{"type": "think", "think": "先看 imports"}),
			event("ContentPart", map[string]any{"type": "text", "text": "好的，我来重构。"}),
			event("ToolCall", map[string]any{
				"type": "function", "id": "tc-1",
				"function": map[string]any{"name": "Shell", "arguments": `{"cmd":"ls"}`},
			}),
			// noise to verify we skip it
			event("StatusUpdate", map[string]any{"context_usage": 0.42}),
			event("StepBegin", map[string]any{"n": 1}),
			event("ToolResult", map[string]any{
				"tool_call_id": "tc-1",
				"return_value": map[string]any{"is_error": false, "output": "main.go\nutil.go"},
			}),
			event("ContentPart", map[string]any{"type": "text", "text": "看完了。"}),
			event("TurnEnd", map[string]any{}),
			// Turn 2: user input is a ContentPart[] (multimodal), tool errors
			event("TurnBegin", map[string]any{"user_input": []map[string]any{
				{"type": "text", "text": "再看 util.go"},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,..."}},
			}}),
			event("ToolCall", map[string]any{
				"type": "function", "id": "tc-2",
				"function": map[string]any{"name": "Read", "arguments": `{"path":"util.go"}`},
			}),
			event("ToolResult", map[string]any{
				"tool_call_id": "tc-2",
				"return_value": map[string]any{"is_error": true, "output": "no such file"},
			}),
		},
	)

	src := New(home)
	refs, _ := src.List(context.Background(), time.Time{})
	if len(refs) != 1 {
		t.Fatalf("refs: %d", len(refs))
	}
	sess, err := src.Load(context.Background(), refs[0])
	if err != nil || sess == nil {
		t.Fatalf("Load: %v sess=%v", err, sess)
	}

	if sess.Agent != "kimi-code" || sess.ID != "kimi-code/ses-demo" {
		t.Errorf("agent/id: %s / %s", sess.Agent, sess.ID)
	}
	if sess.Title != "kimi flow" || sess.CWD != "/root/proj" || sess.Model != "kimi-k2" {
		t.Errorf("state.json metadata not applied: %+v", sess)
	}
	if sess.StartedAt.IsZero() {
		t.Error("StartedAt should be populated from createdAt (1781256000000 ms)")
	}

	// 4 messages: user, assistant (turn 1), user, assistant (turn 2)
	if len(sess.Messages) != 4 {
		t.Fatalf("messages: %d, want 4. got: %+v", len(sess.Messages), summarize(sess.Messages))
	}

	// turn 1 user
	if sess.Messages[0].Role != model.RoleUser || sess.Messages[0].Text != "重构这段代码" {
		t.Errorf("turn1 user: %+v", sess.Messages[0])
	}

	// turn 1 assistant: think prefix + text + tool call
	a1 := sess.Messages[1]
	if a1.Role != model.RoleAssistant {
		t.Errorf("turn1 assistant role: %s", a1.Role)
	}
	if !strings.Contains(a1.Text, "[think] 先看 imports") {
		t.Errorf("think part missing: %q", a1.Text)
	}
	if !strings.Contains(a1.Text, "看完了。") {
		t.Errorf("post-tool text missing: %q", a1.Text)
	}
	if len(a1.ToolCalls) != 1 || a1.ToolCalls[0].Name != "Shell" || a1.ToolCalls[0].Output != "main.go\nutil.go" {
		t.Errorf("turn1 toolcalls: %+v", a1.ToolCalls)
	}

	// turn 2 user: ContentPart[] decoded → text + [image] tag
	if !strings.Contains(sess.Messages[2].Text, "再看 util.go") {
		t.Errorf("turn2 user text: %q", sess.Messages[2].Text)
	}
	if !strings.Contains(sess.Messages[2].Text, "[image]") {
		t.Errorf("turn2 image marker missing: %q", sess.Messages[2].Text)
	}

	// turn 2 assistant: error gets [error] prefix
	a2 := sess.Messages[3]
	if len(a2.ToolCalls) != 1 || !strings.HasPrefix(a2.ToolCalls[0].Output, "[error]") {
		t.Errorf("turn2 error wrapping: %+v", a2.ToolCalls)
	}
}

func TestLoadSkipsBookkeepingAndRequestLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")
	seedSession(t, home, "wd_x", "ses-noise", "", []string{
		event("TurnBegin", map[string]any{"user_input": "hi"}),
		// pure noise — none of these should affect message count
		event("StatusUpdate", map[string]any{}),
		event("CompactionBegin", map[string]any{}),
		event("CompactionEnd", map[string]any{}),
		event("PlanDisplay", map[string]any{"plan": "do x"}),
		event("HookTriggered", map[string]any{"name": "pre"}),
		event("HookResolved", map[string]any{"name": "pre"}),
		event("SubagentEvent", map[string]any{}),
		event("BtwBegin", map[string]any{}),
		event("BtwEnd", map[string]any{}),
		event("SteerInput", map[string]any{"user_input": "mid-stream"}),
		event("StepInterrupted", map[string]any{}),
		event("StepRetry", map[string]any{}),
		// request lines (UI dialogs, not content)
		`{"jsonrpc":"2.0","method":"request","id":"r1","params":{"type":"ApprovalRequest","payload":{}}}`,
		event("ContentPart", map[string]any{"type": "text", "text": "ok"}),
		event("TurnEnd", map[string]any{}),
	})

	refs, _ := New(home).List(context.Background(), time.Time{})
	if len(refs) != 1 {
		t.Fatalf("refs: %d", len(refs))
	}
	sess, err := New(home).Load(context.Background(), refs[0])
	if err != nil || sess == nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sess.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d: %v", len(sess.Messages), summarize(sess.Messages))
	}
	if sess.Messages[1].Text != "ok" {
		t.Errorf("assistant text: %q", sess.Messages[1].Text)
	}
}

func TestLoadEmptyWireReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")
	// Whitespace-only wire.jsonl: must not crash, must yield no ref.
	seedSession(t, home, "wd_empty", "ses-empty", `{"title":""}`, []string{""})
	refs, _ := New(home).List(context.Background(), time.Time{})
	if len(refs) != 0 {
		t.Errorf("empty wire.jsonl should not surface as a ref, got %d", len(refs))
	}
}

func summarize(ms []model.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m.Role) + ":" + strings.TrimSpace(m.Text)
	}
	return out
}
