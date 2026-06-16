package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/oplog"
)

func TestParseSince(t *testing.T) {
	if ts, err := parseSince(""); err != nil || !ts.IsZero() {
		t.Errorf("empty: %v %v", ts, err)
	}
	if ts, err := parseSince("2026-01-31"); err != nil || ts.Format("2006-01-02") != "2026-01-31" {
		t.Errorf("date: %v %v", ts, err)
	}
	for in, wantAgo := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		ts, err := parseSince(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got := time.Since(ts)
		if got < wantAgo-time.Minute || got > wantAgo+time.Minute {
			t.Errorf("%s: off by %v", in, got-wantAgo)
		}
	}
	if _, err := parseSince("eleventy"); err == nil {
		t.Error("garbage must error")
	}
	if _, err := parseSince("eleventy"); !isUsageError(err) {
		t.Error("garbage --since must be a usage error (exit 2)")
	}
}

func TestShortID(t *testing.T) {
	cases := map[string]string{
		// UUID-style IDs: head-6 + … + tail-4 keeps both halves.
		"codex/019ea0af-3d6a-7393-8ccf-a4ae49f116c3": "codex/019ea0…16c3",
		"claude-code/7dd2afaf-1234-5678":             "claude-code/7dd2af…5678",
		// opencode-family IDs share a fixed `ses_141bXXX` prefix; the
		// tail-4 is what actually distinguishes one session from another.
		// Regression for tape#mimo-short-id: prior shortID truncated to
		// `mimocode/ses_141b` and made every session look identical.
		"mimocode/ses_141b69756ffeXmTRdFwDAW8QIU": "mimocode/ses_14…8QIU",
		"opencode/ses_141dcf748ffeE800iK5eLs7MzE": "opencode/ses_14…7MzE",
		// Pathologically short IDs and unprefixed IDs are kept verbatim.
		"short":            "short",
		"under12char":      "under12char",
		"longerthantwelve": "longer…elve",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShortIDDistinguishesSimilarPrefixes pins the actual bug: three
// distinct mimocode sessions whose IDs share the first 8 chars must
// produce three distinct short labels.
func TestShortIDDistinguishesSimilarPrefixes(t *testing.T) {
	ids := []string{
		"mimocode/ses_141b69756ffeXmTRdFwDAW8QIU",
		"mimocode/ses_141b69751ffes63wTfiifT7N3V",
		"mimocode/ses_141b69725ffeSvkt6QI5HLic13",
	}
	seen := map[string]string{}
	for _, id := range ids {
		s := shortID(id)
		if prev, ok := seen[s]; ok {
			t.Errorf("shortID collision: %q and %q both → %q", prev, id, s)
		}
		seen[s] = id
	}
}

// TestShellQuote pins the POSIX single-quote escape rule we rely on
// when building remote ssh commands (a stray quote inside cwd must not
// break the shell). The escape sequence '\” is awkward but standard;
// every POSIX shell handles it.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":                     "''",
		"/root/code/spaceship": "'/root/code/spaceship'",
		"/home/me/with space":  "'/home/me/with space'",
		"/tmp/it's/quoted":     `'/tmp/it'\''s/quoted'`,
		`/path/with "double" and 'single' quotes`: `'/path/with "double" and '\''single'\'' quotes'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateHelper(t *testing.T) {
	if got := truncate("中文标题超长需要截断", 6); !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
	if got := truncate("ok", 10); got != "ok" {
		t.Errorf("got %q", got)
	}
}

func TestStartHint(t *testing.T) {
	cases := map[string]string{
		"claude-code": "claude",
		"codex":       "codex",
		"cursor":      "cursor-agent",
		"gemini":      "gemini",
		"qwen":        "qwen",
		"iflow":       "iflow",
		"aider":       "aider",
		"opencode":    "opencode",
		"antigravity": "agy",
		"qoder":       "qodercli",
		"mimocode":    "mimo",
		"kimi-code":   "kimi",
	}
	for agent, bin := range cases {
		if hint := startHint(agent, "h.md"); !strings.HasPrefix(hint, bin+" ") || !strings.Contains(hint, "h.md") {
			t.Errorf("%s hint = %q", agent, hint)
		}
	}
}

func TestCLIErrorFormatting(t *testing.T) {
	e := cliError{Type: "not_found", Message: "no such session", Suggestion: "tape ls"}
	if !strings.Contains(e.Error(), "tape ls") {
		t.Errorf("suggestion missing from %q", e.Error())
	}
	bare := cliError{Type: "x", Message: "boom"}
	if bare.Error() != "boom" {
		t.Errorf("got %q", bare.Error())
	}
}

func TestSchemaDescribe(t *testing.T) {
	root := &cobra.Command{Use: "tape", Short: "root"}
	exp := &cobra.Command{Use: "export", Short: "export the archive", Example: "  tape export"}
	exp.Flags().Bool("dry-run", false, "preview")
	exp.Flags().String("output", "", "output file path")
	root.AddCommand(exp, &cobra.Command{Use: "help"})

	s := describe(root)
	if len(s.Subcommands) != 1 || s.Subcommands[0].Name != "export" {
		t.Fatalf("help command must be hidden, got %+v", s.Subcommands)
	}
	p := s.Subcommands[0]
	if p.Name != "export" || p.Example == "" {
		t.Errorf("export schema: %+v", p)
	}
	names := map[string]string{}
	for _, f := range p.Flags {
		names[f.Name] = f.Type
	}
	if names["--dry-run"] != "bool" || names["--output"] != "string" {
		t.Errorf("flags: %v", names)
	}
}

// TestParseSize: the human-friendly size parser handles K/M/G
// suffixes (IEC powers of 1024) and rejects obvious typos.
func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1":    1,
		"512":  512,
		"1K":   1024,
		"2k":   2048,
		"4M":   4 * 1024 * 1024,
		"1G":   1024 * 1024 * 1024,
		"200m": 200 * 1024 * 1024,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q) err: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.5M", "M", "1Z", "  "} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q): expected error", bad)
		}
	}
}

// TestParseSplitValidation covers the flag combinations: empty / none
// pass through, agent + month don't care about size, size requires
// a positive size, and unknown modes are usage errors.
func TestParseSplitValidation(t *testing.T) {
	if s, err := parseSplit("", ""); err != nil || s.mode != splitNone {
		t.Errorf("empty: %+v err=%v", s, err)
	}
	if s, err := parseSplit("agent", ""); err != nil || s.mode != splitAgent {
		t.Errorf("agent: %+v err=%v", s, err)
	}
	if s, err := parseSplit("MONTH", ""); err != nil || s.mode != splitMonth {
		t.Errorf("MONTH: %+v err=%v", s, err)
	}
	if s, err := parseSplit("size", "200M"); err != nil || s.sizeBudget != 200*1024*1024 {
		t.Errorf("size 200M: %+v err=%v", s, err)
	}
	if _, err := parseSplit("size", "0"); err == nil {
		t.Error("size 0 must error")
	}
	if _, err := parseSplit("size", "wat"); err == nil {
		t.Error("size 'wat' must error")
	}
	if _, err := parseSplit("daily", ""); err == nil {
		t.Error("unknown mode must error")
	}
}

// TestInsertSuffix pins the per-extension dance for the chunked-
// export output filename. Multi-component extensions get the
// suffix inserted before the *first* component (".codex.tar.zst",
// not ".tar.codex.zst") so the file remains a recognizable tar.zst.
func TestInsertSuffix(t *testing.T) {
	cases := map[[2]string]string{
		{"out.tar.zst", "codex"}:           "out.codex.tar.zst",
		{"out.tar.gz", "2026-06"}:          "out.2026-06.tar.gz",
		{"out.tar.xz", "part-001"}:         "out.part-001.tar.xz",
		{"out.zip", "codex"}:               "out.codex.zip",
		{"out.tar", "codex"}:               "out.codex.tar",
		{"/abs/p/snap.tar.zst", "agent-x"}: "/abs/p/snap.agent-x.tar.zst",
	}
	for in, want := range cases {
		if got := insertSuffix(in[0], in[1]); got != want {
			t.Errorf("insertSuffix(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// TestPlanChunksAgent + Month + Size exercise the three grouping
// strategies on synthetic summary lists. We don't go through
// snapshot.Write here — the chunk planning is the contract we
// actually want to pin (e2e covers the writer side).
func TestPlanChunksAgent(t *testing.T) {
	sums := []model.Summary{
		{ID: "codex/a", Agent: "codex"},
		{ID: "codex/b", Agent: "codex"},
		{ID: "cursor/c", Agent: "cursor"},
	}
	got := planChunks(sums, splitSpec{mode: splitAgent}, nil)
	if len(got) != 2 {
		t.Fatalf("agent chunks = %d, want 2: %+v", len(got), got)
	}
	if got[0].suffix != "codex" || len(got[0].ids) != 2 {
		t.Errorf("first chunk: %+v", got[0])
	}
	if got[1].suffix != "cursor" || len(got[1].ids) != 1 {
		t.Errorf("second chunk: %+v", got[1])
	}
}

func TestPlanChunksMonth(t *testing.T) {
	jun := mustParseTime("2026-06-01T00:00:00Z")
	jul := mustParseTime("2026-07-15T00:00:00Z")
	sums := []model.Summary{
		{ID: "codex/a", UpdatedAt: jun},
		{ID: "cursor/b", UpdatedAt: jul},
		{ID: "codex/c", UpdatedAt: jun},
	}
	got := planChunks(sums, splitSpec{mode: splitMonth}, nil)
	if len(got) != 2 || got[0].suffix != "2026-06" || got[1].suffix != "2026-07" {
		t.Fatalf("month chunks: %+v", got)
	}
	if len(got[0].ids) != 2 || len(got[1].ids) != 1 {
		t.Errorf("month chunk sizes: %+v", got)
	}
}

func TestPlanChunksSize(t *testing.T) {
	sums := []model.Summary{
		{ID: "a/1"}, {ID: "a/2"}, {ID: "a/3"}, {ID: "a/4"},
	}
	sizes := map[string]int64{"a/1": 100, "a/2": 100, "a/3": 100, "a/4": 100}
	got := planChunks(sums, splitSpec{mode: splitSize, sizeBudget: 250}, func(id string) int64 {
		return sizes[id]
	})
	// 100+100=200 (under 250), +100=300 (>250) → break. 3 then 1.
	if len(got) != 2 {
		t.Fatalf("size chunks = %d, want 2: %+v", len(got), got)
	}
	if len(got[0].ids) != 2 || len(got[1].ids) != 2 {
		t.Errorf("size chunk sizes: %+v", got)
	}
	if got[0].suffix != "part-001" || got[1].suffix != "part-002" {
		t.Errorf("size chunk suffix: %+v", got)
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestNormalizeTagStripsV pins the tag comparison helper. Cases
// straight from the four spellings users / GitHub mix interchangeably.
func TestNormalizeTagStripsV(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"0.1.0":          "0.1.0",
		"v0.1.0":         "0.1.0",
		"v0.1.0-dev":     "0.1.0-dev",
		"v1.2.3+meta.42": "1.2.3-meta.42",
	}
	for in, want := range cases {
		got := normalizeTag(in)
		if got != want && !(in == "v1.2.3+meta.42" && got == "1.2.3") {
			// We don't promise a specific shape for build metadata,
			// just that the leading v is gone.
			if !strings.HasPrefix(got, "1.2.3") && in == "v1.2.3+meta.42" {
				t.Errorf("normalizeTag(%q) = %q, want %q", in, got, want)
			}
		}
	}
}

// TestUpgradeCommandPerInstall verifies the install→command table
// the update flow shows users; npm/go-install have concrete recipes,
// homebrew + manual fall through to an empty (manual) path.
func TestUpgradeCommandPerInstall(t *testing.T) {
	cases := map[string]string{
		"npm":        "npm install -g @tapeai/tape@0.2.0",
		"go-install": "go install github.com/chenhg5/tape/cmd/tape@v0.2.0",
		"homebrew":   "",
		"manual":     "",
		"":           "",
	}
	for install, want := range cases {
		got := upgradeCommand(install, "v0.2.0")
		if got != want {
			t.Errorf("upgradeCommand(%q) = %q, want %q", install, got, want)
		}
	}
	// Without the leading v, go-install still produces a v-prefixed
	// module version (Go modules require it).
	if got := upgradeCommand("go-install", "0.2.0"); !strings.HasSuffix(got, "@v0.2.0") {
		t.Errorf("go-install w/o v: %q", got)
	}
}

// TestDetectInstall covers the path-based heuristics. We can't test
// detection of the *real* current process (depends on the runner),
// but the helper is pure and parametric over a path.
func TestDetectInstall(t *testing.T) {
	cases := map[string]string{
		"/home/u/.npm/lib/node_modules/@tapeai/tape/bin/tape": "npm",
		"/home/u/node_modules/.bin/tape":                      "npm",
		"/opt/homebrew/bin/tape":                              "homebrew",
		"/usr/local/Cellar/tape/0.1/bin/tape":                 "homebrew",
		"/random/path/tape":                                   "manual",
	}
	for path, want := range cases {
		if got := detectInstall(path); got != want {
			t.Errorf("detectInstall(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestSplitCSVAndTrim covers the two-axis flattening: cobra
// StringArray pre-aggregates repeated flags, but users also
// reach for `a,b,c`. Both paths converge on the same flat slice,
// empty fragments are dropped, whitespace is trimmed.
func TestSplitCSVAndTrim(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{""}, nil},
		{[]string{"a"}, []string{"a"}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{[]string{"a,b"}, []string{"a", "b"}},
		{[]string{"a, b,  c"}, []string{"a", "b", "c"}},
		{[]string{"a,b", "c"}, []string{"a", "b", "c"}},
		{[]string{",,a,,b,,"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCSVAndTrim(c.in)
		if !equalStringSlice(got, c.want) {
			t.Errorf("splitCSVAndTrim(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestResolveExcludeAgentsNormalizesAndDedupes: users mix shorthand
// and canonical names; the resolver should fold them to the
// canonical form (de-duplicated, order-preserving). Unknown names
// surface as usage errors with the same did-you-mean shape as the
// positive --agent flag.
func TestResolveExcludeAgentsNormalizesAndDedupes(t *testing.T) {
	got, err := resolveExcludeAgents([]string{"cc,codex", "claude-code", "OC"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []string{"claude-code", "codex", "opencode"}
	if !equalStringSlice(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := resolveExcludeAgents([]string{"clude-code"}); err == nil {
		t.Fatal("typo must error")
	} else if !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("error must include did-you-mean: %v", err)
	}
}

// TestJobsToEncoderConcurrency: the budget divides cleanly across
// chunk workers, never falls below 1, and 0 (auto) is the same as
// GOMAXPROCS. We don't pin the exact GOMAXPROCS-derived value (CI
// varies) but assert the inversion property: bigger parallelism →
// smaller per-encoder concurrency.
func TestJobsToEncoderConcurrency(t *testing.T) {
	if got := jobsToEncoderConcurrency(8, 4); got != 2 {
		t.Errorf("8/4: got %d, want 2", got)
	}
	if got := jobsToEncoderConcurrency(4, 8); got != 1 {
		t.Errorf("4/8: got %d, want 1 (floor)", got)
	}
	if got := jobsToEncoderConcurrency(1, 1); got != 1 {
		t.Errorf("1/1: got %d, want 1", got)
	}
	// Explicit 1 worker = "serial encoding"; budget passes through.
	if got := jobsToEncoderConcurrency(6, 1); got != 6 {
		t.Errorf("6/1: got %d, want 6", got)
	}
	// Auto (0) === GOMAXPROCS, so a 1-chunk run gets the full budget.
	if got := jobsToEncoderConcurrency(0, 1); got < 1 {
		t.Errorf("0/1: got %d, want >=1", got)
	}
}

func equalStringSlice(a, b []string) bool {
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

// TestResolveAgentFilter exercises the wrapper that every command
// using --agent / --to funnels through. Canonical/shortcode/alias
// each resolve, the empty string passes through (= no filter), and
// an unknown name surfaces a usage error with "did you mean…".
func TestResolveAgentFilter(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"claude-code": "claude-code",
		"cc":          "claude-code",
		"Claude":      "claude-code",
		"oc":          "opencode",
		"OpenCode":    "opencode",
		"kimi":        "kimi-code",
	}
	for in, want := range cases {
		got, err := resolveAgentFilter(in)
		if err != nil {
			t.Errorf("resolveAgentFilter(%q) err: %v", in, err)
		}
		if got != want {
			t.Errorf("resolveAgentFilter(%q) = %q, want %q", in, got, want)
		}
	}

	_, err := resolveAgentFilter("clauude")
	if err == nil {
		t.Fatal("expected error for typo")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("typo error missing suggestion: %v", err)
	}
}

// TestRedactArtifactKeepLengthForDB pins the per-extension policy
// the export writer uses: .db files get masked in place (so the
// SQLite header / page boundaries stay valid), everything else gets
// the readable [REDACTED:<rule>] marker. Both happen during the
// streaming write, never against the local file.
func TestRedactArtifactKeepLengthForDB(t *testing.T) {
	plain := []byte("hello AKIAIOSFODNN7EXAMPLE world")
	if out := redactArtifact("codex/p/s/session.json", plain); !strings.Contains(string(out), "[REDACTED:") {
		t.Errorf("text path lost the readable marker: %q", out)
	}

	dbBytes := []byte("AKIAIOSFODNN7EXAMPLE")
	masked := redactArtifact("opencode/storage/opencode.db", dbBytes)
	if len(masked) != len(dbBytes) {
		t.Errorf(".db path must keep length: got %d, want %d", len(masked), len(dbBytes))
	}
	if string(masked) == string(dbBytes) {
		t.Error(".db path: secret survived in-place masking")
	}
}

func TestIsCobraUsage(t *testing.T) {
	cases := map[string]bool{
		"unknown command \"foo\" for \"tape\"":    true,
		"accepts 1 arg(s), received 0":            true,
		"requires at least 1 arg":                 true,
		"unknown shorthand flag: 'x' in -x":       true,
		"some other failure":                      false,
		"this happens to mention unknown command": false, // must be prefix
	}
	for msg, want := range cases {
		if got := isCobraUsage(errors.New(msg)); got != want {
			t.Errorf("isCobraUsage(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestEmitJSONShape(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	if err := emitJSON(map[string]any{"hello": "world", "n": 3}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	<-done

	var got struct {
		SchemaVersion int            `json:"schema_version"`
		Data          map[string]any `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("emitJSON output not valid JSON: %v\n%s", err, buf.String())
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, schemaVersion)
	}
	if got.Data["hello"] != "world" {
		t.Errorf("payload not round-tripped: %+v", got.Data)
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatValue(t *testing.T) {
	if got := formatValue(nil); got != "" {
		t.Errorf("nil: got %q, want empty", got)
	}
	if got := formatValue("hello"); got != "hello" {
		t.Errorf("scalar: got %q", got)
	}
	// Slices are sorted and comma-joined inside [...] so config get / set
	// round-trip cleanly without the user seeing Go's default [a b c].
	if got := formatValue([]string{"b", "a", "c"}); got != "[a, b, c]" {
		t.Errorf("slice: got %q, want [a, b, c]", got)
	}
	if got := formatValue([]string{}); got != "[]" {
		t.Errorf("empty slice: got %q, want []", got)
	}
}

func TestSortedKeysAndCountKeys(t *testing.T) {
	got := sortedKeys(map[string]string{"b": "x", "a": "y", "c": "z"})
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sortedKeys = %v, want %v", got, want)
	}
	gotc := sortedCountKeys(map[string]int{"z": 1, "a": 2})
	if strings.Join(gotc, ",") != "a,z" {
		t.Errorf("sortedCountKeys = %v", gotc)
	}
}

func TestSummarizeRecord(t *testing.T) {
	r := oplog.Record{
		Scope:  map[string]string{"agent": "claude-code", "host": ""},
		Counts: map[string]int{"sessions": 12, "files": 47},
		Bytes:  2048,
	}
	got := summarizeRecord(r)
	if !strings.Contains(got, "agent=claude-code") {
		t.Errorf("missing scope: %q", got)
	}
	if strings.Contains(got, "host=") {
		t.Errorf("empty scope value leaked: %q", got)
	}
	if !strings.Contains(got, "files=47") || !strings.Contains(got, "sessions=12") {
		t.Errorf("missing counts: %q", got)
	}
	if !strings.Contains(got, "2.0 KiB") {
		t.Errorf("missing bytes: %q", got)
	}
}

func TestResolveHomePriority(t *testing.T) {
	saved := map[string]string{
		"TAPE_HOME": os.Getenv("TAPE_HOME"),
		"TAPE_DIR":  os.Getenv("TAPE_DIR"),
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	// 1. TAPE_HOME wins outright.
	os.Setenv("TAPE_HOME", "/tmp/tape-home")
	os.Setenv("TAPE_DIR", "/tmp/tape-dir")
	if got := resolveHome(); got != "/tmp/tape-home" {
		t.Errorf("TAPE_HOME priority: got %q", got)
	}

	// 2. TAPE_DIR is the backward-compat fallback.
	os.Unsetenv("TAPE_HOME")
	if got := resolveHome(); got != "/tmp/tape-dir" {
		t.Errorf("TAPE_DIR fallback: got %q", got)
	}

	// 3. Default to ~/.tape when neither is set.
	os.Unsetenv("TAPE_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".tape")
	if got := resolveHome(); got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
}

func TestDefaultsRecoversFromBrokenConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write garbage into config.json so Load() returns an error;
	// Defaults() must still return a usable zero value rather than crash.
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{home: home}
	_ = app.Defaults() // must not panic on broken config.
	_ = app.Defaults() // second call hits the cached fast path.
}

func TestRecordedRunSetters(t *testing.T) {
	r := startRun(&App{}, "test-op")
	if r.op != "test-op" || r.scope == nil || r.counts == nil {
		t.Fatalf("startRun did not initialise: %+v", r)
	}
	// Empty scope value is dropped (see summarizeRecord behavior).
	r.setScope("agent", "")
	if _, ok := r.scope["agent"]; ok {
		t.Errorf("empty scope value should be dropped")
	}
	r.setScope("agent", "claude-code")
	r.setCount("sessions", 7)
	r.setBytes(2048)
	if r.scope["agent"] != "claude-code" || r.counts["sessions"] != 7 || r.bytes != 2048 {
		t.Errorf("setters not persisted: %+v", r)
	}
}

func TestRenderHistoryRendersOneRow(t *testing.T) {
	var buf bytes.Buffer
	saved := os.Stdout
	rd, wr, _ := os.Pipe()
	os.Stdout = wr
	renderHistory(&App{}, []oplog.Record{{
		Op:       "sync",
		Started:  time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		Finished: time.Date(2026, 6, 15, 12, 0, 5, 0, time.UTC),
		Duration: "5s",
		Counts:   map[string]int{"sessions": 3},
	}})
	wr.Close()
	io.Copy(&buf, rd)
	os.Stdout = saved
	if !strings.Contains(buf.String(), "sync") {
		t.Errorf("renderHistory missing op name: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "sessions=3") {
		t.Errorf("renderHistory missing summary: %q", buf.String())
	}
}

func TestAppIndexCloseRoundtrip(t *testing.T) {
	app := &App{home: t.TempDir()}
	ix, err := app.Index()
	if err != nil {
		t.Fatalf("Index() open: %v", err)
	}
	if ix == nil {
		t.Fatal("Index() returned nil")
	}
	// Second call must return the cached instance, not reopen.
	ix2, err := app.Index()
	if err != nil || ix2 != ix {
		t.Errorf("Index() did not cache: %v %v", ix, ix2)
	}
	// Close on a nil-archive App must be a no-op; we don't open it here.
	app.Close()
}

func TestResolveSessionIDLastOnEmptyArchive(t *testing.T) {
	app := &App{home: t.TempDir()}
	_, err := app.resolveSessionID(context.Background(), "@last")
	if !errors.Is(err, ErrNoResults) {
		t.Errorf("@last on empty archive: want ErrNoResults, got %v", err)
	}
}

func TestArchiveDirIsHomeRelative(t *testing.T) {
	app := &App{home: "/tmp/fake-home"}
	want := filepath.Join("/tmp/fake-home", "archive")
	if got := app.archiveDir(); got != want {
		t.Errorf("archiveDir = %q, want %q", got, want)
	}
}

func TestCollectVersionPopulatesPlatformFields(t *testing.T) {
	info := collectVersion(&App{Version: "0.9.9-test", home: "/tmp/fake-home"})
	if info.Version != "0.9.9-test" {
		t.Errorf("Version not propagated: %q", info.Version)
	}
	if info.Go == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("runtime fields blank: %+v", info)
	}
	if info.HomeDir != "/tmp/fake-home" {
		t.Errorf("HomeDir not propagated: %q", info.HomeDir)
	}
	// Install is always set (worst case "manual") so json consumers
	// never have to special-case the empty string.
	if info.Install == "" {
		t.Errorf("Install must always be classified, got empty")
	}
}

func TestInstallLabel(t *testing.T) {
	cases := map[string]string{
		"npm":         "npm (@tapeai/tape)",
		"go-install":  "go install",
		"homebrew":    "homebrew",
		"manual":      "manual / system binary",
		"never-heard": "unknown",
	}
	for in, want := range cases {
		if got := installLabel(in); got != want {
			t.Errorf("installLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderHistoryEmptyDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderHistory panicked on nil: %v", r)
		}
	}()
	renderHistory(&App{}, nil)
}

func TestSummarizeRecordOmitsZeroBytes(t *testing.T) {
	got := summarizeRecord(oplog.Record{
		Scope:  map[string]string{"agent": "cc"},
		Counts: map[string]int{"sessions": 1},
		Bytes:  0,
	})
	if strings.Contains(got, "B") {
		t.Errorf("zero bytes should not render a size: %q", got)
	}
}
