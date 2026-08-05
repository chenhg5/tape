package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/agentid"
	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// hitsPerSession caps how many matching messages are shown per session,
// so one chatty conversation can't dominate the screen. The full hit list
// is still in --json output.
const hitsPerSession = 3

// renderOpts collects the display knobs for renderHits. Kept as a struct
// so the (currently small) call sites in command + interactive picker
// don't drift apart when we add the next flag.
type renderOpts struct {
	// snippetWidth is the maximum display width for a single-line
	// snippet. Computed from the terminal width by default. Ignored
	// when expand is true (multi-line wrap follows terminal width
	// directly).
	snippetWidth int
	// expand turns the single-line snippet into a soft-wrapped
	// paragraph that respects the full window. Use when the user
	// asks for "show me what this match actually said", not just
	// the headline.
	expand bool
	// context is the grep -C N value: print N turns of conversation
	// before and after each matching turn, in dim color. 0 disables.
	context int
	// archive lets the renderer pull full message bodies for
	// context lines; nil means context==0 effectively.
	archive interface {
		Get(context.Context, string) (*model.Session, error)
	}
	ctx context.Context
}

// renderHits prints search results grouped by session. Layout:
//
//	▸ <session-id>  <agent>  <relative-time>  ·  <title>
//	    <role>  <snippet with query highlighted>
//	      [-C N: prev N turns, dim]
//	    <role>  <snippet with query highlighted>
//	      [-C N: next N turns, dim]
//	    <gray>  + N more in this session
//
//	▸ <next session>
//	    ...
//
// Sessions appear in the order the index returned them (newest-first by
// default, BM25 when --sort relevance is set).
func renderHits(app *App, hits []ports.Hit, query string, page int, hasMore bool, opts renderOpts) {
	app.lead()
	if opts.snippetWidth <= 0 {
		// Terminal width minus role column (9) + indent (4) + a
		// safety margin so wrapped CJK doesn't push the column off
		// the right edge. Floor at 100 so the contract on narrow
		// terminals doesn't regress below the previous hardcoded
		// width, ceiling at 200 to keep ultra-wide monitors from
		// generating un-scannable wall-of-text rows.
		w := termCols(140) - 14
		switch {
		case w < 100:
			w = 100
		case w > 200:
			w = 200
		}
		opts.snippetWidth = w
	}
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

		// Resolve full session ONCE per group if any of the
		// context-aware features is on — both -C and --expand need
		// the original messages, and re-Getting per hit would re-
		// read session.json N times for no benefit.
		var sess *model.Session
		if (opts.context > 0 || opts.expand) && opts.archive != nil {
			sess, _ = opts.archive.Get(opts.ctx, id)
		}

		shown := g.hits
		if len(shown) > hitsPerSession {
			shown = shown[:hitsPerSession]
		}
		for hi, hh := range shown {
			// -C N: pre-context (older turns)
			if opts.context > 0 && sess != nil {
				printContextWindow(app, sess, hh.MessageID,
					-opts.context, -1)
			}
			// The hit line itself: either single-line (default)
			// or wrapped multi-line (--expand). The yellow
			// highlight per query term is preserved in both.
			role := app.cyan(padRightDisp(hh.Role, 9))
			body := pickHitBody(sess, hh)
			if opts.expand {
				printExpandedHit(app, role, body, query, opts.snippetWidth)
			} else {
				fmt.Printf("    %s  %s\n",
					role,
					highlightSnippet(app, body, query, opts.snippetWidth))
			}
			// -C N: post-context (newer turns)
			if opts.context > 0 && sess != nil {
				printContextWindow(app, sess, hh.MessageID,
					1, opts.context)
			}
			// Separator between hits within the same session
			// when context is on, otherwise hits stack tightly.
			if opts.context > 0 && hi < len(shown)-1 {
				fmt.Println()
			}
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

// pickHitBody returns the text we should render for this hit. When the
// archive lookup succeeded we prefer the full message body (because
// --expand needs more than the index snippet has) and fall back to the
// index-side snippet otherwise. Picking is per-hit, so a session whose
// .Get failed still gets reasonable single-line output for every hit.
func pickHitBody(sess *model.Session, h ports.Hit) string {
	if sess == nil || h.MessageID == "" {
		return h.Snippet
	}
	for _, m := range sess.Messages {
		if m.ID == h.MessageID {
			if m.Text != "" {
				return m.Text
			}
		}
	}
	return h.Snippet
}

// printExpandedHit renders one hit as a soft-wrapped paragraph. We wrap
// at snippetWidth and indent continuation lines so the role column stays
// readable. Highlighting is applied AFTER wrapping so terms split across
// a wrap boundary still bold; the trade-off is that we wrap on display
// width but highlight by string content — for our terms (CJK, prefix
// matches) the visual difference is negligible.
func printExpandedHit(app *App, role, body, query string, width int) {
	body = collapseWhitespace(body)
	// Reserve the role + indent budget so wrapped continuation lines
	// align under the snippet, not the role column.
	const rolePrefix = "    "
	const contIndent = "              " // 4 spaces indent + 9 role width + 2 spaces
	lines := wrapDisplayWidth(body, width)
	for i, line := range lines {
		highlighted := highlightTerms(app, line, query)
		if i == 0 {
			fmt.Printf("%s%s  %s\n", rolePrefix, role, highlighted)
		} else {
			fmt.Printf("%s%s\n", contIndent, highlighted)
		}
	}
}

// printContextWindow prints a slice of session messages relative to the
// hit's MessageID position: offsetFrom..offsetTo (inclusive, signed —
// negative is "before", positive is "after"). Lines are dim, prefixed
// with a relative marker like "↑2" / "↓1" so the user can navigate to
// the right place in `tape show`. The hit line itself (offset==0) is
// never printed here; renderHits owns it.
func printContextWindow(app *App, sess *model.Session, hitMsgID string, offsetFrom, offsetTo int) {
	idx := -1
	for i, m := range sess.Messages {
		if m.ID == hitMsgID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	width := termCols(140) - 18
	if width < 60 {
		width = 60
	}
	if width > 200 {
		width = 200
	}
	for off := offsetFrom; off <= offsetTo; off++ {
		if off == 0 {
			continue
		}
		i := idx + off
		if i < 0 || i >= len(sess.Messages) {
			continue
		}
		m := sess.Messages[i]
		marker := "↑"
		if off > 0 {
			marker = "↓"
		}
		marker = fmt.Sprintf("%s%d", marker, abs(off))
		text := collapseWhitespace(m.Text)
		if text == "" && len(m.ToolCalls) > 0 {
			text = fmt.Sprintf("(tool call: %s)", m.ToolCalls[0].Name)
		}
		// Truncate context lines tighter than hit lines so the eye
		// still gravitates to the match. -C is for orientation, not
		// reading every adjacent turn end-to-end.
		text = truncDisp(text, width)
		fmt.Printf("        %s %s  %s\n",
			app.gray(marker),
			app.dim(padRightDisp(string(m.Role), 9)),
			app.dim(text))
	}
}

// printContextWindow needs a small ints helper without pulling in math.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// highlightTerms is the multi-term wrapper around caseInsensitiveReplace
// used by both single-line and wrapped renderers; keeping the loop in
// one place means a future "max highlights per line" cap lands in one
// spot.
func highlightTerms(app *App, s, query string) string {
	for _, t := range queryTerms(query) {
		if t == "" {
			continue
		}
		s = caseInsensitiveReplace(s, t, app.yellow(t))
	}
	return s
}

// wrapDisplayWidth breaks s into lines of at most width display cells.
// Honors CJK character widths via display-width-aware iteration. Breaks
// on existing whitespace when possible; falls back to hard breaks for
// long runs of CJK or unbroken latin (e.g. URLs).
func wrapDisplayWidth(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	runes := []rune(s)
	var line []rune
	lineW := 0
	lastBreak := -1
	for _, r := range runes {
		rw := runeDisplayWidth(r)
		if lineW+rw > width {
			// prefer break at last whitespace
			if lastBreak >= 0 && lastBreak < len(line) {
				lines = append(lines, strings.TrimRight(string(line[:lastBreak]), " "))
				rest := line[lastBreak:]
				line = append([]rune{}, rest...)
				// recompute lineW
				lineW = 0
				for _, rr := range line {
					lineW += runeDisplayWidth(rr)
				}
				lastBreak = -1
			} else {
				lines = append(lines, string(line))
				line = line[:0]
				lineW = 0
			}
		}
		line = append(line, r)
		lineW += rw
		if r == ' ' {
			lastBreak = len(line)
		}
	}
	if len(line) > 0 {
		lines = append(lines, strings.TrimRight(string(line), " "))
	}
	return lines
}

// runeDisplayWidth returns 2 for east-asian wide chars, 1 otherwise.
// Approximation good enough for snippet wrapping; not aiming for full
// Unicode UAX#11 fidelity.
func runeDisplayWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f:
		return 0
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x9FFF,  // CJK
		r >= 0xA000 && r <= 0xA4CF,  // Yi
		r >= 0xAC00 && r <= 0xD7A3,  // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,  // CJK Compatibility
		r >= 0xFE30 && r <= 0xFE4F,  // CJK Compatibility forms
		r >= 0xFF00 && r <= 0xFF60,  // Fullwidth
		r >= 0xFFE0 && r <= 0xFFE6,  // Fullwidth signs
		r >= 0x20000 && r <= 0x2FFFD,
		r >= 0x30000 && r <= 0x3FFFD:
		return 2
	}
	return 1
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
	var excludeAgent, excludeDir, excludeHost []string
	var limit, page, ctxLines int
	var printOnly, expand bool
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

For more context per hit:
  --expand / -E       wrap the snippet across multiple lines instead of
                      truncating to one; you see the full message body
                      (still highlighted) without diving into 'tape show'.
  --context N / -C N  grep-style: print N user/assistant turns before
                      and after each matching turn, dim-coloured, with
                      ↑/↓N relative-position markers. Useful when one
                      turn alone doesn't tell you whether this is the
                      conversation you wanted.

Results are ordered newest-session-first (--sort recent, default) because
the most useful match is usually "the conversation I just had". Switch to
--sort relevance for classic BM25 ranking when you are mining old archives.`,
		Example: `  tape search "auth migration"             # interactive picker on a TTY
  tape search "auth" --print               # grouped list, single-line snippet
  tape search "auth" --print --expand      # multi-line wrap, full body
  tape search "auth" --print -C 2          # grep-style: 2 turns of context each side
  tape search codex --agent cursor
  tape search "memory leak" --sort relevance --limit 50`,
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
			if ctxLines < 0 {
				return usageErrf("--context must be >= 0")
			}
			dir = resolveDirFilter(dir)
			excludedAgents, err := resolveExcludeAgents(mergeExcludeAgents(app, excludeAgent))
			if err != nil {
				return err
			}
			excludedDirs := resolveExcludeDirs(mergeExcludeDirs(app, excludeDir))
			excludedHosts := splitCSVAndTrim(mergeExcludeHosts(app, excludeHost))
			// over-fetch by one to detect "has more" without a separate count
			// query (FTS COUNT(*) over the same MATCH would be expensive).
			hits, err := ix.Search(cmd.Context(), ports.Query{
				Text:            strings.Join(args, " "),
				Agent:           agentCanon,
				Project:         dir,
				Host:            host,
				Since:           t,
				Sort:            sort,
				Limit:           limit + 1,
				Offset:          (page - 1) * limit,
				ExcludeAgents:   excludedAgents,
				ExcludeProjects: excludedDirs,
				ExcludeHosts:    excludedHosts,
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
					return noResultsForSearch(cmd.Context(), ix)
				}
				return nil
			}
			if len(hits) == 0 {
				return noResultsForSearch(cmd.Context(), ix)
			}
			query := strings.Join(args, " ")
			// Interactive path mirrors `tape ls`: TTY + no --print + no
			// explicit pagination → group hits by session and let the
			// user pick + act in one go, no copy-paste needed.
			if !printOnly && app.interactive() && !cmd.Flag("page").Changed {
				return runInteractiveSearch(cmd.Context(), app, query, hits)
			}
			renderHits(app, hits, query, page, hasMore, renderOpts{
				expand:  expand,
				context: ctxLines,
				archive: app.Archive(),
				ctx:     cmd.Context(),
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "filter by agent — full name or 2-letter shorthand ("+agentid.HelpLine()+")")
	cmd.Flags().StringVar(&dir, "dir", "", "filter by project directory ('.' = current dir)")
	cmd.Flags().StringVar(&host, "host", "", `filter by origin host ("local" or ssh-host)`)
	cmd.Flags().StringArrayVar(&excludeAgent, "exclude-agent", nil, "exclude these agents (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeDir, "exclude-dir", nil, "exclude these project dirs (repeatable, or comma-separated)")
	cmd.Flags().StringArrayVar(&excludeHost, "exclude-host", nil, `exclude these origin hosts ("local" = drop local-only; repeatable)`)
	cmd.Flags().StringVar(&since, "since", "", "only sessions updated since (24h, 7d, 2026-01-31)")
	cmd.Flags().StringVar(&sort, "sort", "recent", "result order: 'recent' (newest session first) or 'relevance' (BM25)")
	cmd.Flags().IntVar(&limit, "limit", 20, "page size")
	cmd.Flags().IntVar(&page, "page", 1, "page number (1-based)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "skip the interactive picker, just print the grouped hit list")
	cmd.Flags().BoolVarP(&expand, "expand", "E", false, "wrap each hit across multiple lines instead of truncating to one (shows the full message body)")
	cmd.Flags().IntVarP(&ctxLines, "context", "C", 0, "grep-style: print N turns before AND after each matching turn, dim-coloured")
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
			app.cyan(padRightDisp(shortID(id), shortIDColW)),
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
