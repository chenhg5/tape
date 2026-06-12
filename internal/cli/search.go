package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/core/ports"
)

// renderHits prints search results grouped by session, with the matching
// query highlighted in snippets. Layout per hit:
//
//	▸ <session-id>  <agent>  <relative-time>
//	  <title>
//	    <role>  <snippet with query highlighted>
//
// Sessions with multiple matching messages share one header.
func renderHits(app *App, hits []ports.Hit, query string) {
	type bySession struct {
		first ports.Hit
		more  []ports.Hit
	}
	groups := make(map[string]*bySession)
	order := []string{}
	for _, h := range hits {
		g, ok := groups[h.SessionID]
		if !ok {
			g = &bySession{first: h}
			groups[h.SessionID] = g
			order = append(order, h.SessionID)
			continue
		}
		g.more = append(g.more, h)
	}

	for i, id := range order {
		g := groups[id]
		h := g.first
		title := h.Title
		if title == "" {
			title = h.Project
		}
		when := relTime(h.Timestamp)
		if h.Timestamp.IsZero() {
			when = "" // some adapters don't carry per-message timestamps
		}
		header := fmt.Sprintf("%s %s  %s",
			app.gray("▸"),
			app.bold(shortID(h.SessionID)),
			app.agentColor(h.Agent))
		if when != "" {
			header += "  " + app.gray(when)
		}
		fmt.Println(header)
		if title != "" {
			fmt.Printf("  %s\n", truncDisp(title, 76))
		}
		all := append([]ports.Hit{h}, g.more...)
		for _, hh := range all {
			fmt.Printf("    %s  %s\n",
				app.dim(padRightDisp(hh.Role, 9)),
				highlightSnippet(app, hh.Snippet, query, 100))
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
	fmt.Printf("\n%s\n", app.gray(fmt.Sprintf(
		"%d hit(s) in %d %s. tape show <id> to replay.",
		len(hits), sessionCount, noun)))
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
	var agent, project, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across all archived sessions",
		Long:  "Search every archived session. Latin words match by prefix; Chinese/Japanese/Korean text is matched by character bigrams, so CJK queries just work.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			ix, err := app.Index()
			if err != nil {
				return err
			}
			hits, err := ix.Search(cmd.Context(), ports.Query{
				Text:    strings.Join(args, " "),
				Agent:   agent,
				Project: project,
				Since:   t,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			if app.useJSON() {
				if hits == nil {
					hits = []ports.Hit{} // JSON [] not null
				}
				if err := emitJSON(map[string]any{"hits": hits, "count": len(hits)}); err != nil {
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
			renderHits(app, hits, strings.Join(args, " "))
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent")
	cmd.Flags().StringVar(&project, "project", "", "filter by project path")
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max hits")
	return cmd
}
