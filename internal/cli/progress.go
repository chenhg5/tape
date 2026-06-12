package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Progress is a TTY-aware status line. On a TTY it renders a colored bar
// that overwrites itself in place; on a pipe or with NO_COLOR it stays
// silent so logs and agent stdout remain clean.
//
// Usage:
//
//	pb := app.newProgress("syncing", 0) // unknown total -> spinner mode
//	pb.SetTotal(123)
//	pb.Update(7, "claude-code/abc")
//	pb.Done("synced 123 session(s)")
type Progress struct {
	w         io.Writer
	enabled   bool
	color     bool
	label     string
	total     int64
	current   int64
	note      string
	started   time.Time
	lastDraw  time.Time
	mu        sync.Mutex
	spinFrame int
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// newProgress wires a reporter to stderr (stdout stays JSON-clean). It
// no-ops in non-TTY / --json / NO_COLOR mode.
func (a *App) newProgress(label string, total int64) *Progress {
	enabled := !a.useJSON() && a.useColor() && isStderrTTY()
	return &Progress{
		w:       os.Stderr,
		enabled: enabled,
		color:   a.useColor(),
		label:   label,
		total:   total,
		started: time.Now(),
	}
}

// isStderrTTY checks whether stderr is a character device (a terminal).
// Cargo/npm use the same heuristic.
func isStderrTTY() bool {
	st, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// SetTotal updates the denominator (0 = indeterminate / spinner mode).
func (p *Progress) SetTotal(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = n
	p.draw(true)
}

// Update sets current progress and an optional note (e.g. current file).
func (p *Progress) Update(n int64, note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = n
	p.note = note
	p.draw(false)
}

// Inc is shorthand for current++.
func (p *Progress) Inc(note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current++
	p.note = note
	p.draw(false)
}

// Done clears the bar and prints a final message on its own line.
func (p *Progress) Done(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled {
		fmt.Fprint(p.w, "\r\x1b[2K") // clear current line
	}
	if msg != "" {
		fmt.Fprintln(p.w, msg)
	}
}

// draw renders the current state. Coalesces redraws to <=20fps so a
// tight loop doesn't waste cycles repainting.
func (p *Progress) draw(force bool) {
	if !p.enabled {
		return
	}
	now := time.Now()
	if !force && now.Sub(p.lastDraw) < 50*time.Millisecond {
		return
	}
	// First paint: leave a blank line above the bar so it doesn't sit
	// flush against the user's previous prompt / command output. `Done`
	// only clears the bar's own line, so this padding survives the run
	// and continues to separate the bar's final note from history.
	if p.lastDraw.IsZero() {
		fmt.Fprintln(p.w)
	}
	p.lastDraw = now

	var line string
	if p.total > 0 {
		line = p.renderBar()
	} else {
		line = p.renderSpinner()
	}
	fmt.Fprint(p.w, "\r\x1b[2K"+line)
}

func (p *Progress) renderBar() string {
	const width = 24
	pct := float64(p.current) / float64(p.total)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	if p.current > 0 && filled == 0 {
		filled = 1
	}
	bar := "[" + colorize(p.color, "32",
		repeat("█", filled)) + repeat("░", width-filled) + "]"
	right := fmt.Sprintf("%d/%d", p.current, p.total)
	line := fmt.Sprintf("%s  %s  %s",
		colorize(p.color, "1", p.label), bar, colorize(p.color, "90", right))
	if p.note != "" {
		line += "  " + colorize(p.color, "2", truncDisp(p.note, 40))
	}
	return line
}

func (p *Progress) renderSpinner() string {
	frame := spinner[p.spinFrame%len(spinner)]
	p.spinFrame++
	elapsed := time.Since(p.started).Truncate(time.Second).String()
	line := fmt.Sprintf("%s %s  %s",
		colorize(p.color, "36", string(frame)),
		colorize(p.color, "1", p.label),
		colorize(p.color, "90", elapsed))
	if p.note != "" {
		line += "  " + colorize(p.color, "2", truncDisp(p.note, 40))
	}
	return line
}

// colorize wraps s in an ANSI SGR code when color is enabled.
func colorize(on bool, code, s string) string {
	if !on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
