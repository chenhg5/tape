package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/agentid"
	"github.com/chenhg5/tape/internal/core/ports"
)

// hitsPerSession caps how many matching messages are shown per session,
// so one chatty conversation can't dominate the screen. The full hit list
// is still in --json output.
const hitsPerSession = 3

// renderHits prints search results grouped by session. Layout:
//
//	▸ <session-id>  <agent>  <relative-time>  ·  <title>
//	    <role>  <snippet with query highlighted>
//	    <role>  <snippet with query highlighted>
//	    <gray>  + N more in this session
//
//	▸ <next session>
//	    ...
//
// Sessions appear in the order the index returned them (newest-first by
// default, BM25 when --sort relevance is set).
func renderHits(app *App, hits []ports.Hit, query string, page int, hasMore bool) {
	app.lead()
	type group struct {
		hits []ports.Hit
	}
	groups := make(map[string]*group)
	order := []string{}
	for _, h := range hits {
		g, ok := groups[h.SessionID]
		if !ok {
			g = &group{}
			groups[h.SessionID] = g
			order = append(order, h.SessionID)
		}
		g.hits = append(g.hits, h)
	}

	for i, id := range order {
		g := groups[id]
		h := g.hits[0]
		title := h.Title
		if title == "" {
			title = h.Project
		}
		when := relTime(h.Timestamp)
		if h.Timestamp.IsZero() {
			when = ""
		}
		header := fmt.Sprintf("%s %s  %s",
			app.gray("▸"),
			app.bold(shortID(h.SessionID)),
			app.agentColor(h.Agent))
		if when != "" {
			header += "  " + app.gray(when)
		}
		if title != "" {
			header += "  " + app.gray("·") + "  " + app.dim(truncDisp(title, 56))
		}
		fmt.Println(header)

		shown := g.hits
		if len(shown) > hitsPerSession {
			shown = shown[:hitsPerSession]
		}
		for _, hh := range shown {
			fmt.Printf("    %s  %s\n",
				app.cyan(padRightDisp(hh.Role, 9)),
				highlightSnippet(app, hh.Snippet, query, 100))
		}
		if extra := len(g.hits) - len(shown); extra > 0 {
			fmt.Printf("    %s\n",
				app.gray(fmt.Sprintf("+ %d more match(es) — tape show %s",
					extra, shortID(h.SessionID))))
		}
		if i < len(order)-1 {
			fmt.Println()
		}
	}

	sessionCount := len(order)
	noun := "session"
	if sessionCount != 1 {
		noun = "sessions"
	}
	footer := fmt.Sprintf("%d hit(s) in %d %s", len(hits), sessionCount, noun)
	if page > 1 || hasMore {
		footer += fmt.Sprintf("  ·  page %d", page)
	}
	if hasMore {
		footer += fmt.Sprintf("  ·  --page %d for more", page+1)
	}
	fmt.Printf("\n%s\n", app.gray(footer+".  tape show <id> to replay."))
}

// highlightSnippet collapses whitespace, truncates around the query match
// and bolds occurrences of the query terms. Whitespace-only queries fall
// back to plain truncation.
func highlightSnippet(app *App, snippet, query string, width int) string {
	s := collapseWhitespace(snippet)
	s = strings.TrimPrefix(s, "…")
	s = strings.TrimSuffix(s, "…")
	s = truncDisp(s, width)

	terms := queryTerms(query)
	if len(terms) == 0 {
		return s
	}
	for _, t := range terms {
		if t == "" {
			continue
		}
		s = caseInsensitiveReplace(s, t, app.yellow(t))
	}
	return s
}

// queryTerms strips FTS operators so we only highlight literal terms.
func queryTerms(q string) []string {
	q = strings.ReplaceAll(q, "\"", " ")
	q = strings.ReplaceAll(q, "*", " ")
	q = strings.ReplaceAll(q, "(", " ")
	q = strings.ReplaceAll(q, ")", " ")
	var out []string
	for _, w := range strings.Fields(q) {
		if w == "AND" || w == "OR" || w == "NOT" {
			continue
		}
		out = append(out, w)
	}
	return out
}

func caseInsensitiveReplace(s, old, replacement string) string {
	if old == "" {
		return s
	}
	lower := strings.ToLower(s)
	lowerOld := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], lowerOld)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		b.WriteString(s[i : i+j])
		b.WriteString(replacement)
		i += j + len(lowerOld)
	}
}

func newSearchCmd(app *App) *cobra.Command {
	var agent, dir, since, sort, host string
	var limit, page int
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across all archived sessions",
		Long: `Search every archived session. Latin words match by prefix; Chinese,
Japanese and Korean text is matched by character bigrams, so CJK queries
just work.

On a TTY hits are grouped by session and presented as an interactive
picker: ↑/↓ to browse, Enter to act on a session (Resume here / Show /
Copy ID / Copy resume command). Pass --print for the legacy grouped
list output, or --json / pipe for the machine contract.

Results are ordered newest-session-first (--sort recent, default) because
the most useful match is usually "the conversation I just had". Switch to
--sort relevance for classic BM25 ranking when you are mining old archives.`,
		Example: `  tape search "auth migration"            # interactive picker on a TTY
  tape search codex --agent cursor
  tape search "memory leak" --sort relevance --limit 50
  tape search "auth" --print               # legacy grouped list`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageErrf("missing search query. try:\n" +
					"  tape search \"auth migration\"\n" +
					"  tape search codex          (matches by agent, title or content)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			agentCanon, err := resolveAgentFilter(agent)
			if err != nil {
				return err
			}
			ix, err := app.Index()
			if err != nil {
				return err
			}
			if page < 1 {
				return usageErrf("--page must be >= 1")
			}
			if limit < 1 {
				return usageErrf("--limit must be >= 1")
			}
			switch sort {
			case "", "recent", "relevance":
			default:
				return usageErrf("--sort must be 'recent' or 'relevance'")
			}
			dir = resolveDirFilter(dir)
			// over-fetch by one to detect "has more" without a separate count
			// query (FTS COUNT(*) over the same MATCH would be expensive).
			hits, err := ix.Search(cmd.Context(), ports.Query{
				Text:    strings.Join(args, " "),
				Agent:   agentCanon,
				Project: dir,
				Host:    host,
				Since:   t,
				Sort:    sort,
				Limit:   limit + 1,
				Offset:  (page - 1) * limit,
			})
			if err != nil {
				return err
			}
			hasMore := len(hits) > limit
			if hasMore {
				hits = hits[:limit]
			}
			if app.useJSON() {
				if hits == nil {
					hits = []ports.Hit{}
				}
				if err := emitJSON(map[string]any{
					"hits": hits, "count": len(hits),
					"page": page, "page_size": limit, "has_more": hasMore,
				}); err != nil {
					return err
				}
				if len(hits) == 0 {
					return ErrNoResults
				}
				return nil
			}
			if len(hits) == 0 {
				return ErrNoResults
			}
			query := strings.Join(args, " ")
			// Interactive path mirrors `tape ls`: TTY + no --print + no
			// explicit pagination → group hits by session and let the
			// user pick + act in one go, no copy-paste needed.
			if !printOnly && app.interactive() && !cmd.Flag("page").Changed {
				return runInteractiveSearch(cmd.Context(), app, query, hits)
			}
			renderHits(app, hits, query, page, hasMore)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent — full name or 2-letter shorthand ("+agentid.HelpLine()+")")
	cmd.Flags().StringVar(&dir, "dir", "", "filter by project directory ('.' = current dir)")
	cmd.Flags().StringVar(&host, "host", "", `filter by origin host ("local" or ssh-host)`)
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().StringVar(&sort, "sort", "recent", "result order: 'recent' (newest session first) or 'relevance' (BM25)")
	cmd.Flags().IntVar(&limit, "limit", 20, "page size")
	cmd.Flags().IntVar(&page, "page", 1, "page number (1-based)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "skip the interactive picker, just print the grouped hit list")
	return cmd
}

// runInteractiveSearch groups hits by session, shows them in a picker,
// then runs the same action menu as `tape ls`. The first matching hit
// per session becomes the picker label (snippet with the query bolded);
// the "+N more" tail tells the user the session has additional matches
// they'd see in `tape show <id>`.
func runInteractiveSearch(ctx context.Context, app *App, query string, hits []ports.Hit) error {
	type group struct {
		first ports.Hit
		count int
	}
	groups := map[string]*group{}
	var order []string
	for _, h := range hits {
		if g, ok := groups[h.SessionID]; ok {
			g.count++
			continue
		}
		groups[h.SessionID] = &group{first: h, count: 1}
		order = append(order, h.SessionID)
	}

	labels := make([]string, len(order))
	for i, id := range order {
		g := groups[id]
		// Use the snippet — not the title — as the headline so the
		// user sees *why* this session matched. Title still appears
		// dimmed at the end when present and distinct, so context is
		// kept; truncation budgets are tuned for an 80-col terminal
		// after the id/agent/time prefix (~50 cols).
		snippet := highlightSnippet(app, g.first.Snippet, query, 50)
		more := ""
		if g.count > 1 {
			more = "  " + app.gray(fmt.Sprintf("+%d more", g.count-1))
		}
		// Remote-origin hits: prefix snippet with a dim @host badge
		// so the picker mirrors `tape ls` and the user knows ahead of
		// time that Resume will SSH there.
		if g.first.Host != "" {
			snippet = "@" + g.first.Host + "  " + snippet
		}
		labels[i] = fmt.Sprintf("%s  %s  %s  %s%s",
			app.cyan(padRightDisp(shortID(id), 22)),
			app.agentColor(padRightDisp(g.first.Agent, 11)),
			padRightDisp(relTime(g.first.Timestamp), 9),
			snippet, more)
	}
	pick, err := pickInteractive(app, fmt.Sprintf("Pick a session (%d matches):", len(hits)), labels)
	if err != nil {
		if errors.Is(err, errPromptAborted) {
			return nil
		}
		return err
	}
	sess, err := app.Archive().Get(ctx, order[pick])
	if err != nil {
		return err
	}
	return runSessionAction(app, sess)
}
