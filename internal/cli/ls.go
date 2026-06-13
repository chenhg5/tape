package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/agentid"
	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// resolveDirFilter normalises the --dir filter into the absolute path the
// archive matches against. "." (and the empty string when caller wishes)
// expand to the working directory; a relative path becomes absolute so the
// archive's prefix match works regardless of where the user invokes tape.
// Non-existent paths are still passed through verbatim: users may filter
// for an old project that no longer lives on disk.
func resolveDirFilter(raw string) string {
	if raw == "" {
		return ""
	}
	if raw == "." {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return raw
	}
	if abs, err := filepath.Abs(raw); err == nil {
		return abs
	}
	return raw
}

// resolveAgentFilter normalizes whatever the user typed for --agent
// / --to (`cc`, `Claude`, `claude_code`, …) into the canonical
// Source.Name() string. An empty input passes through unchanged so
// callers can keep using "" to mean "no agent filter".
//
// On an unknown name we don't silently treat it as a no-op (that
// would mask typos and quietly return everything); we surface a
// usage error with the closest matches the suggester finds. The
// short codes are listed in the help line so users discover them
// without having to read the source.
func resolveAgentFilter(raw string) (string, error) {
	canon, ok := agentid.Normalize(raw)
	if ok {
		return canon, nil
	}
	hint := ""
	if guesses := agentid.Suggest(raw, 3); len(guesses) > 0 {
		hint = " — did you mean " + strings.Join(guesses, ", ") + "?"
	}
	return "", usageErrf("unknown agent %q%s\n  known: %s", raw, hint, agentid.HelpLine())
}

// mergeExcludeAgents folds the user's --exclude-agent CLI input
// with whatever defaults.exclude_agents is set in ~/.tape/config.json.
// Order: defaults first (so they read first in config list output),
// then CLI; resolveExcludeAgents de-dupes downstream. Logic is
// "CLI is additive on top of config" — config is a baseline
// preference, the flag is a per-invocation increment. This matches
// the mole / gh convention where personal defaults stay in config
// and one-off overrides ride the command line.
func mergeExcludeAgents(app *App, fromCLI []string) []string {
	defs := app.Defaults().ExcludeAgents
	if len(defs) == 0 {
		return fromCLI
	}
	out := make([]string, 0, len(defs)+len(fromCLI))
	out = append(out, defs...)
	out = append(out, fromCLI...)
	return out
}

func mergeExcludeDirs(app *App, fromCLI []string) []string {
	defs := app.Defaults().ExcludeDirs
	if len(defs) == 0 {
		return fromCLI
	}
	out := make([]string, 0, len(defs)+len(fromCLI))
	out = append(out, defs...)
	out = append(out, fromCLI...)
	return out
}

func mergeExcludeHosts(app *App, fromCLI []string) []string {
	defs := app.Defaults().ExcludeHosts
	if len(defs) == 0 {
		return fromCLI
	}
	out := make([]string, 0, len(defs)+len(fromCLI))
	out = append(out, defs...)
	out = append(out, fromCLI...)
	return out
}

// splitCSVAndTrim accepts the StringArray values cobra hands us and
// flattens both axes — repeated flags (`--exclude-agent cc
// --exclude-agent oc`) and comma-separated values (`--exclude-agent
// cc,oc`) — into one slice. Empty fragments are dropped. The CSV
// half is what users reach for first; the repeat half is for cases
// where commas are unsafe (shell expansion, completions).
func splitCSVAndTrim(raw []string) []string {
	var out []string
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// resolveExcludeAgents normalizes a list of user-typed agent names
// (shorthands, aliases, wonky casing) into canonical Source.Name()
// strings. Same suggester-on-typo behavior as resolveAgentFilter so
// the error path is consistent across positive and negative flags.
// The returned slice is de-duplicated to keep downstream SQL/loops
// tidy.
func resolveExcludeAgents(raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, item := range splitCSVAndTrim(raw) {
		canon, ok := agentid.Normalize(item)
		if !ok {
			hint := ""
			if guesses := agentid.Suggest(item, 3); len(guesses) > 0 {
				hint = " — did you mean " + strings.Join(guesses, ", ") + "?"
			}
			return nil, usageErrf("unknown agent %q%s\n  known: %s", item, hint, agentid.HelpLine())
		}
		if !seen[canon] {
			seen[canon] = true
			out = append(out, canon)
		}
	}
	return out, nil
}

// resolveExcludeDirs runs every entry through resolveDirFilter so
// relative paths and "." get the same absolute-path treatment as
// --dir. We don't reject anything here — resolveDirFilter handles
// the validation and just returns the raw string when normalization
// fails — but we do drop empties from CSV splits.
func resolveExcludeDirs(raw []string) []string {
	var out []string
	for _, item := range splitCSVAndTrim(raw) {
		out = append(out, resolveDirFilter(item))
	}
	return out
}

// renderSessionList prints sessions in a tight, colored table with
// rune-aware column widths. Columns: id · agent · updated · msgs · title.
// Project is shown as a subtle prefix on the title when it differs from
// the title, to save horizontal space.
func renderSessionList(app *App, sums []model.Summary, page, pageSize, total int) {
	const idW, agentW, timeW, msgsW = 22, 11, 9, 6
	app.lead()

	header := fmt.Sprintf("  %s  %s  %s  %s  %s",
		padRightDisp("ID", idW),
		padRightDisp("AGENT", agentW),
		padRightDisp("UPDATED", timeW),
		padRightDisp("MSGS", msgsW),
		"TITLE")
	fmt.Println(app.gray(header))

	for _, s := range sums {
		title := s.Title
		if title == "" {
			title = s.Project
		}
		titleCol := truncDisp(title, 60)
		if s.Project != "" && s.Title != "" {
			titleCol = app.gray(truncDisp(s.Project, 28)) + "  " + titleCol
		}
		// Prefix remote rows with a dim @host tag so the local/remote
		// split is obvious without a dedicated column (most users have
		// zero remote rows; a column would be wasted space).
		if s.Host != "" {
			titleCol = app.gray("@"+s.Host) + "  " + titleCol
		}
		id := padRightDisp(shortID(s.ID), idW)
		agent := padRightDisp(s.Agent, agentW)
		fmt.Printf("  %s  %s  %s  %s  %s\n",
			app.cyan(shortID(s.ID))+id[len(shortID(s.ID)):],
			app.agentColor(s.Agent)+agent[len(s.Agent):],
			padRightDisp(relTime(s.UpdatedAt), timeW),
			padRightDisp(fmt.Sprintf("%d", s.MsgCount), msgsW),
			titleCol)
	}
	footer := fmt.Sprintf("%d of %d session(s)", len(sums), total)
	if total > pageSize {
		pages := (total + pageSize - 1) / pageSize
		footer += fmt.Sprintf("  ·  page %d/%d", page, pages)
		if page*pageSize < total {
			footer += fmt.Sprintf("  ·  --page %d for more", page+1)
		}
	}
	fmt.Printf("\n%s\n", app.gray(footer+".  tape show <id> to replay."))
}

func newLsCmd(app *App) *cobra.Command {
	var agent, dir, since, host string
	var excludeAgent, excludeDir, excludeHost []string
	var limit, page int
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List archived sessions (interactive on a TTY)",
		Long: `List archived sessions, newest first.

On a TTY this drops you into an interactive picker: ↑/↓ to browse,
Enter to act on a session (Resume here / Show transcript / Copy ID /
Copy resume command). Pass --print to keep the legacy plain table,
e.g. for scripts or screenshots. --json and piped output always emit
the table-equivalent JSON contract.

--dir <path> restricts the list to sessions whose working directory was
<path> (or one of its descendants). Use "." for the current directory —
the most common filter, e.g. "tape ls --dir ." inside a repo.

--host <name> scopes the list by origin machine. Pass "local" to hide
sessions mirrored in via 'tape sync --remote', or an SSH destination
(matching what you used in --remote) to see only that host's sessions.
Remote rows are tagged with a dim @host badge in the output.

Pagination: --limit N --page P returns page P (1-based) of N items each.
The interactive picker honors --limit (defaults to 30 there) but
ignores --page; for deep browsing combine --print with --page.`,
		Example: `  tape ls                            # interactive picker on a TTY
  tape ls --dir .                    # sessions for the current repo
  tape ls --agent codex
  tape ls --print --limit 20 --page 3  # third page, plain table`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if page < 1 {
				return usageErrf("--page must be >= 1")
			}
			if limit < 1 {
				return usageErrf("--limit must be >= 1")
			}
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
			agentCanon, err := resolveAgentFilter(agent)
			if err != nil {
				return err
			}
			dir = resolveDirFilter(dir)
			excludedAgents, err := resolveExcludeAgents(mergeExcludeAgents(app, excludeAgent))
			if err != nil {
				return err
			}
			excludedDirs := resolveExcludeDirs(mergeExcludeDirs(app, excludeDir))
			excludedHosts := splitCSVAndTrim(mergeExcludeHosts(app, excludeHost))
			// Interactive path: TTY, no --json, no --print, no
			// explicit pagination. The picker uses a single page of
			// `limit` items (defaults to 30 — see below); pagination is
			// the table path's affordance.
			if !printOnly && app.interactive() && !cmd.Flag("page").Changed {
				pickerLimit := limit
				if !cmd.Flag("limit").Changed {
					pickerLimit = 30 // picker can't scroll yet — keep it scrollable-by-eye
				}
				return runInteractiveLs(cmd.Context(), app, ports.Filter{
					Agent: agentCanon, Project: dir, Host: host, Since: sinceTime, Limit: pickerLimit,
					ExcludeAgents: excludedAgents, ExcludeProjects: excludedDirs, ExcludeHosts: excludedHosts,
				})
			}
			filter := ports.Filter{
				Agent: agentCanon, Project: dir, Host: host, Since: sinceTime,
				Limit: limit, Offset: (page - 1) * limit,
				ExcludeAgents: excludedAgents, ExcludeProjects: excludedDirs, ExcludeHosts: excludedHosts,
			}
			sums, err := app.Archive().List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			total, err := app.Archive().Count(cmd.Context(), filter)
			if err != nil {
				return err
			}
			hasMore := page*limit < total
			if app.useJSON() {
				if sums == nil {
					sums = []model.Summary{}
				}
				if err := emitJSON(map[string]any{
					"sessions": sums, "count": len(sums),
					"total": total, "page": page, "page_size": limit, "has_more": hasMore,
				}); err != nil {
					return err
				}
				if len(sums) == 0 {
					return ErrNoResults
				}
				return nil
			}
			if len(sums) == 0 {
				return ErrNoResults
			}
			renderSessionList(app, sums, page, limit, total)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent — full name or 2-letter shorthand ("+agentid.HelpLine()+")")
	cmd.Flags().StringVar(&dir, "dir", "", "filter by project directory ('.' = current dir)")
	cmd.Flags().StringVar(&host, "host", "", `filter by origin host ("local" = this machine, or ssh-host)`)
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().StringArrayVar(&excludeAgent, "exclude-agent", nil, "exclude these agents (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeDir, "exclude-dir", nil, "exclude these project dirs (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeHost, "exclude-host", nil, `exclude these origin hosts ("local" = drop local-only; repeatable)`)
	cmd.Flags().IntVar(&limit, "limit", 20, "page size")
	cmd.Flags().IntVar(&page, "page", 1, "page number (1-based)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "skip the interactive picker, just print the table")
	return cmd
}

// shortID keeps "<agent>/<first-8-of-uuid>" for display; full ids and any
// unique fragment are accepted everywhere ids are read.
// shortID renders a session ID compact enough for picker / table
// columns. It needs to (a) stay under ~22 display columns so the
// picker line doesn't wrap and (b) preserve enough entropy that
// two distinct sessions never collapse to the same label.
//
// Naive "first 8 of sourceID" works for UUID-style IDs (claude-code,
// codex, cursor) but fails on opencode-family IDs (`ses_141bXXXffeYYY`)
// where the first 8 chars are a fixed timestamp prefix and the
// real entropy is at the tail. We split on the first '/' and, if the
// sourceID part is longer than 12, keep "head6 + … + tail4" — that
// captures both the timestamp shard and the random suffix.
func shortID(id string) string {
	abbrev := func(s string) string {
		// Operate on rune slice so multi-byte IDs don't slice mid-rune.
		rs := []rune(s)
		if len(rs) <= 12 {
			return s
		}
		return string(rs[:6]) + "…" + string(rs[len(rs)-4:])
	}
	if i := strings.IndexByte(id, '/'); i > 0 && i+1 < len(id) {
		return id[:i+1] + abbrev(id[i+1:])
	}
	return abbrev(id)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
