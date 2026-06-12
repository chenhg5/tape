package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

const activityDays = 14

type agentStat struct {
	Agent        string    `json:"agent"`
	Sessions     int       `json:"sessions"`
	Messages     int       `json:"messages"`
	LastActivity time.Time `json:"last_activity"`
}

type projectStat struct {
	Project  string `json:"project"`
	Sessions int    `json:"sessions"`
}

type dayStat struct {
	Date     string `json:"date"`
	Sessions int    `json:"sessions"`
}

type overviewStats struct {
	Sessions     int             `json:"sessions"`
	Messages     int             `json:"messages"`
	Projects     int             `json:"projects"`
	ArchiveBytes int64           `json:"archive_bytes"`
	Agents       []agentStat     `json:"agents"`
	Activity     []dayStat       `json:"activity"`
	TopProjects  []projectStat   `json:"top_projects"`
	Recent       []model.Summary `json:"recent"`
}

func newOverviewCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "overview",
		Aliases: []string{"status"},
		Short:   "A bird's-eye view of your archived sessions",
		Example: "  tape overview",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sums, err := app.Archive().List(cmd.Context(), ports.Filter{})
			if err != nil {
				return err
			}
			stats := buildStats(sums, dirSize(app.archiveDir()), time.Now())
			if app.useJSON() {
				return emitJSON(stats)
			}
			renderOverview(app, stats)
			return nil
		},
	}
}

func buildStats(sums []model.Summary, archiveBytes int64, now time.Time) overviewStats {
	st := overviewStats{ArchiveBytes: archiveBytes, Sessions: len(sums)}

	agents := map[string]*agentStat{}
	projects := map[string]int{}
	days := map[string]int{}
	for _, s := range sums {
		st.Messages += s.MsgCount
		a := agents[s.Agent]
		if a == nil {
			a = &agentStat{Agent: s.Agent}
			agents[s.Agent] = a
		}
		a.Sessions++
		a.Messages += s.MsgCount
		if s.UpdatedAt.After(a.LastActivity) {
			a.LastActivity = s.UpdatedAt
		}
		projects[s.Project]++
		if d := now.Sub(s.UpdatedAt); d >= 0 && d < activityDays*24*time.Hour {
			days[s.UpdatedAt.Local().Format("2006-01-02")]++
		}
	}

	for _, a := range agents {
		st.Agents = append(st.Agents, *a)
	}
	sort.Slice(st.Agents, func(i, j int) bool { return st.Agents[i].Sessions > st.Agents[j].Sessions })

	st.Projects = len(projects)
	for p, n := range projects {
		st.TopProjects = append(st.TopProjects, projectStat{Project: p, Sessions: n})
	}
	sort.Slice(st.TopProjects, func(i, j int) bool {
		if st.TopProjects[i].Sessions != st.TopProjects[j].Sessions {
			return st.TopProjects[i].Sessions > st.TopProjects[j].Sessions
		}
		return st.TopProjects[i].Project < st.TopProjects[j].Project
	})
	if len(st.TopProjects) > 5 {
		st.TopProjects = st.TopProjects[:5]
	}

	for i := activityDays - 1; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Local().Format("2006-01-02")
		st.Activity = append(st.Activity, dayStat{Date: date, Sessions: days[date]})
	}

	// sums are already newest-first from the archive
	st.Recent = sums
	if len(st.Recent) > 5 {
		st.Recent = st.Recent[:5]
	}
	return st
}

func renderOverview(app *App, st overviewStats) {
	if st.Sessions == 0 {
		fmt.Println("the archive is empty.")
		fmt.Printf("\n  %s\n\n", app.bold("tape sync"))
		fmt.Println("archives your Claude Code, Codex and Cursor sessions.")
		return
	}

	fmt.Printf("%s · %s · %s · %s\n\n",
		app.bold(fmt.Sprintf("%d sessions", st.Sessions)),
		fmt.Sprintf("%d messages", st.Messages),
		fmt.Sprintf("%d projects", st.Projects),
		humanBytes(st.ArchiveBytes))

	fmt.Println(app.gray("AGENTS"))
	maxSessions := 0
	for _, a := range st.Agents {
		if a.Sessions > maxSessions {
			maxSessions = a.Sessions
		}
	}
	for _, a := range st.Agents {
		name := padRightDisp(a.Agent, 12)
		// re-color only the agent name portion; padding stays plain
		colored := app.agentColor(a.Agent) + name[len(a.Agent):]
		fmt.Printf("  %s %s %4d   %s\n",
			colored,
			app.cyan(bar(a.Sessions, maxSessions, 20)),
			a.Sessions,
			app.gray(relTime(a.LastActivity)))
	}

	fmt.Printf("\n%s\n", app.gray(fmt.Sprintf("ACTIVITY · last %d days", activityDays)))
	counts := make([]int, len(st.Activity))
	peak := 0
	for i, d := range st.Activity {
		counts[i] = d.Sessions
		if d.Sessions > peak {
			peak = d.Sessions
		}
	}
	peakNote := ""
	if peak > 0 {
		peakNote = app.gray(fmt.Sprintf("  peak %d/day", peak))
	}
	fmt.Printf("  %s%s\n", app.green(sparkline(counts)), peakNote)

	fmt.Printf("\n%s\n", app.gray("TOP PROJECTS"))
	for _, p := range st.TopProjects {
		fmt.Printf("  %s %4d\n", padRightDisp(truncDisp(p.Project, 40), 40), p.Sessions)
	}

	fmt.Printf("\n%s\n", app.gray("RECENT"))
	for _, s := range st.Recent {
		title := s.Title
		if title == "" {
			title = s.Project
		}
		id := padRightDisp(shortID(s.ID), 22)
		fmt.Printf("  %s %s %s\n",
			app.cyan(shortID(s.ID))+id[len(shortID(s.ID)):],
			padRightDisp(relTime(s.UpdatedAt), 8),
			truncDisp(title, 48))
	}

	fmt.Printf("\n%s\n", app.gray(`tip: tape search "<keyword>" · tape show <id> · tape restore <id> --to <agent>`))
}

// bar renders a filled/empty proportion like ████████░░░░.
func bar(n, max, width int) string {
	if max <= 0 || width <= 0 {
		return strings.Repeat("░", width)
	}
	filled := n * width / max
	if n > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// sparkline renders counts as a tiny histogram like ▁▂▅▇▃▁▁.
func sparkline(counts []int) string {
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		if max == 0 {
			b.WriteRune(sparks[0])
			continue
		}
		idx := c * (len(sparks) - 1) / max
		if c > 0 && idx == 0 {
			idx = 1
		}
		b.WriteRune(sparks[idx])
	}
	return b.String()
}

// relTime renders "just now", "5m ago", "3h ago", "12d ago".
func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// dirSize sums file sizes under dir; 0 when absent.
func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if _, err := os.Stat(dir); err != nil {
		return 0
	}
	return total
}
