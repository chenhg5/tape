package model

import (
	"testing"
	"time"
)

func TestProjectSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/root/code/spaceship/tape", "root-code-spaceship-tape"},
		{"/root/code/spaceship/tape/", "root-code-spaceship-tape"},
		{"", "unknown"},
		{"/", "unknown"},
		{"C:\\Users\\dev\\proj", "C--Users-dev-proj"},
		{"/path/with space/x", "path-with-space-x"},
		{"relative/dir", "relative-dir"},
		{"/中文/目录", "中文-目录"},
	}
	for _, c := range cases {
		if got := ProjectSlug(c.in); got != c.want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionSummary(t *testing.T) {
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	s := &Session{
		ID: "codex/abc", Agent: "codex", SourceID: "abc",
		Title: "t", CWD: "/root/code/demo",
		StartedAt: start, UpdatedAt: end,
		Messages: []Message{{Role: RoleUser}, {Role: RoleAssistant}},
	}
	sum := s.Summary()
	if sum.ID != "codex/abc" || sum.Agent != "codex" || sum.Title != "t" {
		t.Errorf("identity fields: %+v", sum)
	}
	if sum.Project != "root-code-demo" || sum.CWD != "/root/code/demo" {
		t.Errorf("project fields: %+v", sum)
	}
	if sum.MsgCount != 2 || !sum.StartedAt.Equal(start) || !sum.UpdatedAt.Equal(end) {
		t.Errorf("count/time fields: %+v", sum)
	}
}
