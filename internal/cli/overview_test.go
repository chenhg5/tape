package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/tape/internal/core/model"
)

func TestBar(t *testing.T) {
	cases := []struct {
		n, max, width int
		want          string
	}{
		{10, 10, 4, "████"},
		{5, 10, 4, "██░░"},
		{0, 10, 4, "░░░░"},
		{1, 100, 4, "█░░░"}, // non-zero always shows at least one block
		{3, 0, 4, "░░░░"},   // degenerate max
	}
	for _, c := range cases {
		if got := bar(c.n, c.max, c.width); got != c.want {
			t.Errorf("bar(%d,%d,%d) = %q, want %q", c.n, c.max, c.width, got, c.want)
		}
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]int{0, 0, 0}); got != "▁▁▁" {
		t.Errorf("flat: %q", got)
	}
	got := sparkline([]int{0, 1, 5, 10})
	if utf8.RuneCountInString(got) != 4 {
		t.Errorf("length: %q", got)
	}
	if !strings.HasSuffix(got, "█") {
		t.Errorf("max must hit the top block: %q", got)
	}
	if strings.HasPrefix(got, "█") {
		t.Errorf("zero must stay at the bottom: %q", got)
	}
	// non-zero never renders as the zero glyph
	if rune(got[len("▁"):][0]) == '▁' {
		t.Errorf("small non-zero collapsed to zero glyph: %q", got)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	cases := map[string]time.Time{
		"just now": now.Add(-10 * time.Second),
		"5m ago":   now.Add(-5 * time.Minute),
		"3h ago":   now.Add(-3 * time.Hour),
		"12d ago":  now.Add(-12 * 24 * time.Hour),
		"-":        {},
	}
	for want, in := range cases {
		if got := relTime(in); got != want {
			t.Errorf("relTime(%v) = %q, want %q", in, got, want)
		}
	}
	old := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := relTime(old); got != "2020-01-02" {
		t.Errorf("ancient: %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:                "512 B",
		2048:               "2.0 KB",
		5 * 1024 * 1024:    "5.0 MB",
		3 << 30:            "3.0 GB",
		1536 * 1024 * 1024: "1.5 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), make([]byte, 100), 0o600)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "sub", "b"), make([]byte, 50), 0o600)
	if got := dirSize(dir); got != 150 {
		t.Errorf("dirSize = %d, want 150", got)
	}
	if got := dirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("missing dir: %d", got)
	}
}

func TestBuildStats(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	sum := func(agent, project string, msgs int, updated time.Time) model.Summary {
		return model.Summary{
			ID: agent + "/" + project, Agent: agent, Project: project,
			MsgCount: msgs, UpdatedAt: updated,
		}
	}
	sums := []model.Summary{
		sum("codex", "p1", 10, now.Add(-time.Hour)),
		sum("codex", "p2", 5, now.Add(-26*time.Hour)),
		sum("claude-code", "p1", 7, now.Add(-30*24*time.Hour)), // outside activity window
	}
	st := buildStats(sums, 4096, now)

	if st.Sessions != 3 || st.Messages != 22 || st.Projects != 2 || st.ArchiveBytes != 4096 {
		t.Errorf("totals: %+v", st)
	}
	if len(st.Agents) != 2 || st.Agents[0].Agent != "codex" || st.Agents[0].Sessions != 2 {
		t.Errorf("agents: %+v", st.Agents)
	}
	if st.TopProjects[0].Project != "p1" || st.TopProjects[0].Sessions != 2 {
		t.Errorf("top projects: %+v", st.TopProjects)
	}
	if len(st.Activity) != activityDays {
		t.Fatalf("activity days: %d", len(st.Activity))
	}
	inWindow := 0
	for _, d := range st.Activity {
		inWindow += d.Sessions
	}
	if inWindow != 2 { // the 30-day-old session is excluded
		t.Errorf("activity total = %d, want 2", inWindow)
	}
	if len(st.Recent) != 3 {
		t.Errorf("recent: %d", len(st.Recent))
	}
}

func TestBuildStatsEmpty(t *testing.T) {
	st := buildStats(nil, 0, time.Now())
	if st.Sessions != 0 || len(st.Agents) != 0 || len(st.Activity) != activityDays {
		t.Errorf("empty stats: %+v", st)
	}
}
