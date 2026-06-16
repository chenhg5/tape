// Package mirror abstracts "where do we fetch tape's release
// metadata from", with a built-in latency probe so users in mainland
// China (where github.com can be 10× slower than gitee.com) get a
// fast install + update path without having to know the alias names.
//
// Two hosts ship today:
//
//   - GitHub at chenhg5/tape — canonical, where the npm + go-install
//     story already points.
//   - Gitee at cg33/tape — mirror, fed by the release-time double-
//     push documented in docs/RELEASE.md.
//
// The package is deliberately small: a Host describes one mirror
// (API + asset base + headers + how to parse its release JSON), and
// Pick races a HEAD probe across all registered hosts to choose the
// fastest reachable one. Callers compose this with a normal
// http.Client; nothing here owns network state.
//
// Why race + probe (vs. "always GitHub, fall back to Gitee on
// error"): the failure mode that hurts the most isn't an outright
// connection refusal — it's a 30-second TCP handshake that *might*
// succeed. A probe with a tight timeout (~1.5s) buys "pick fast"
// without paying the worst-case latency tax, which is what we
// actually want for the foreground `tape update` command.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// EnvOverride lets users (and CI) pin a host by name. Recognized
// values: "github", "gitee", "auto" (default). Unknown values are
// treated as "auto" so a typo doesn't brick `tape update`.
const EnvOverride = "TAPE_MIRROR"

// probeTimeout is how long we wait for any one host's HEAD probe to
// answer before giving up on it. Generous enough to clear a 1G
// hotel-wifi spike, tight enough that the slow path (both hosts
// unreachable) doesn't make the user think tape hung.
const probeTimeout = 1500 * time.Millisecond

// fetchTimeout is the per-host budget for the actual API fetch
// after Pick chose a winner. We give it more headroom than the
// probe because parsing the JSON body counts against it too.
const fetchTimeout = 5 * time.Second

// Release is the host-agnostic shape `tape update` consumes. Both
// GitHub and Gitee carry every field; PublishedAt is whichever
// timestamp the host calls "this is when users got it".
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name,omitempty"`
	HTMLURL     string    `json:"html_url,omitempty"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at,omitempty"`
}

// Host is one place we can fetch release metadata from. The struct
// is a value type — Hosts() returns a fresh copy so callers can
// safely tweak fields per-invocation (e.g. swap the repo name in
// a fork build) without mutating the package's defaults.
type Host struct {
	Name        string // short id used in logs + TAPE_MIRROR
	DisplayName string // human label rendered in --debug + history

	// pingURL is a small, always-200 endpoint we HEAD for latency
	// probes. Avoid /releases/latest itself — it can 404 before a
	// first release, which would invalidate the probe.
	pingURL string

	// apiLatest / apiList are full URLs; apiList must accept a
	// per_page query, since beta channel walks the list and takes
	// the first prerelease-aware entry.
	apiLatest string
	apiList   string

	// parser turns the wire bytes into the canonical Release. We
	// give each host its own so Gitee's `created_at` vs GitHub's
	// `published_at` stays a local concern.
	parser func(body []byte) (*Release, error)

	// listParser is the same idea for the beta channel walk.
	listParser func(body []byte) ([]Release, error)

	// userAgent + accept tune the request headers to whatever the
	// host prefers; both API hosts politely 200 on a vanilla GET
	// but GitHub specifically asks for the versioned Accept header.
	userAgent string
	accept    string
}

// Hosts returns the registered mirrors in canonical order
// (GitHub first, Gitee second). The order is the human-facing
// "tape was built against this list" order; Pick races over it,
// so on-the-wire ordering doesn't matter.
func Hosts() []Host {
	return []Host{newGitHubHost(), newGiteeHost()}
}

// Lookup returns the host with the given name (case-insensitive)
// or false if no such host is registered. Used by EnvOverride —
// "TAPE_MIRROR=gitee" hard-pins without probing.
func Lookup(name string) (Host, bool) {
	for _, h := range Hosts() {
		if strings.EqualFold(h.Name, name) {
			return h, true
		}
	}
	return Host{}, false
}

// Pick selects the best host for this caller, in order of priority:
//
//  1. TAPE_MIRROR=<name> if set and recognized.
//  2. The fastest host to respond to a HEAD probe (race).
//  3. Hosts()[0] if every probe failed — better to attempt a
//     real fetch (which will surface a clear error) than to fail
//     Pick itself.
//
// ctx is respected for the probe race; callers should pass a parent
// context with a sensible deadline (or rely on probeTimeout, which
// caps per-host).
func Pick(ctx context.Context) (Host, error) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvOverride))); v != "" && v != "auto" {
		if h, ok := Lookup(v); ok {
			return h, nil
		}
		// Unknown value: fall through to auto rather than failing.
		// "I typo'd TAPE_MIRROR" should not break update.
	}
	hosts := Hosts()
	results := raceProbe(ctx, hosts)
	// Sort ascending by latency; failed probes sink to the end via
	// their math.MaxInt64-ish latency value.
	sort.Slice(results, func(i, j int) bool { return results[i].latency < results[j].latency })
	if len(results) == 0 {
		return Host{}, errors.New("no hosts registered")
	}
	if results[0].err != nil {
		// All hosts errored — return the first registered host
		// (GitHub by convention) so the fetch attempt produces a
		// real, debuggable HTTP error rather than a generic
		// "all probes failed".
		return hosts[0], nil
	}
	return results[0].host, nil
}

// PickReport is the verbose flavor used by --debug. Same selection
// logic as Pick, plus the full per-host probe outcome (latency,
// error) so users can see *why* we chose what we chose.
type PickReport struct {
	Chosen  Host
	Reason  string // "env", "fastest", "fallback"
	Results []ProbeResult
}

// PickWithReport returns the same Host as Pick plus a structured
// report. Use this when you want to log the probe details
// alongside the choice.
func PickWithReport(ctx context.Context) (PickReport, error) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvOverride))); v != "" && v != "auto" {
		if h, ok := Lookup(v); ok {
			return PickReport{Chosen: h, Reason: "env"}, nil
		}
	}
	hosts := Hosts()
	results := raceProbe(ctx, hosts)
	sorted := append([]ProbeResult(nil), results...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].latency < sorted[j].latency })
	rep := PickReport{Results: results}
	if len(sorted) == 0 || sorted[0].err != nil {
		rep.Chosen, rep.Reason = hosts[0], "fallback"
		return rep, nil
	}
	rep.Chosen, rep.Reason = sorted[0].host, "fastest"
	return rep, nil
}

// ProbeResult captures one host's probe outcome. Exported so the
// CLI can render it under --debug without re-running the probe.
type ProbeResult struct {
	host    Host
	latency time.Duration
	err     error
}

// Host returns the host this probe targeted.
func (p ProbeResult) Host() Host { return p.host }

// Latency returns the round-trip we observed; meaningful only when
// Err is nil.
func (p ProbeResult) Latency() time.Duration { return p.latency }

// Err is non-nil when the probe failed; the latency value should
// be treated as "infinity" by sort consumers.
func (p ProbeResult) Err() error { return p.err }

// raceProbe fires one HEAD per host concurrently, with each one
// capped by probeTimeout, and returns every result (in input
// order). Failed probes carry a huge latency value so sorts put
// them at the end naturally.
//
// We don't return early on the first success: the cost of letting
// all probes finish is at most probeTimeout, and we'd rather have
// the full picture for --debug than save 500ms.
func raceProbe(parent context.Context, hosts []Host) []ProbeResult {
	results := make([]ProbeResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h Host) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, probeTimeout)
			defer cancel()
			start := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, h.pingURL, nil)
			if err != nil {
				results[i] = ProbeResult{host: h, latency: probeTimeout * 10, err: err}
				return
			}
			req.Header.Set("User-Agent", h.userAgent)
			resp, err := http.DefaultClient.Do(req)
			elapsed := time.Since(start)
			if err != nil {
				results[i] = ProbeResult{host: h, latency: probeTimeout * 10, err: err}
				return
			}
			resp.Body.Close()
			// Any 2xx / 3xx counts as "reachable"; 4xx/5xx counts
			// as failed (the host is there but unusable).
			if resp.StatusCode >= 400 {
				results[i] = ProbeResult{host: h, latency: probeTimeout * 10,
					err: fmt.Errorf("%s probe %d", h.Name, resp.StatusCode)}
				return
			}
			results[i] = ProbeResult{host: h, latency: elapsed}
		}(i, h)
	}
	wg.Wait()
	return results
}

// FetchLatest pulls the canonical "latest stable" release from this
// host. The channel parameter controls list-walking semantics:
//
//   - "latest" → /releases/latest, returns the most recent non-
//     prerelease.
//   - "beta"   → /releases?per_page=5, returns the first entry
//     (which is the newest, including prereleases).
//
// Any other channel value is a usage error; the CLI validates it
// before reaching here.
func (h Host) FetchLatest(ctx context.Context, channel string) (*Release, error) {
	url := h.apiLatest
	if channel == "beta" {
		url = h.apiList
	}
	rctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", h.userAgent)
	if h.accept != "" {
		req.Header.Set("Accept", h.accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no releases published yet")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s returned %d", h.Name, resp.StatusCode)
	}
	if channel == "beta" {
		list, err := h.listParser(body)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, errors.New("no releases on this channel")
		}
		return &list[0], nil
	}
	return h.parser(body)
}

// APILatest exposes the per-host API URL for the latest stable
// release. Used by --debug ("GET <url>") and by the CLI's user-
// facing error suggestion ("browse <url>"). The list URL is not
// exported because its only sensible caller is FetchLatest itself.
func (h Host) APILatest() string { return h.apiLatest }

// PingURL exposes the probe URL for tests and for the install.sh
// equivalent (which races curl HEADs against this exact URL).
func (h Host) PingURL() string { return h.pingURL }

//
// --- per-host descriptors --------------------------------------
//

// newGitHubHost returns the GitHub mirror. Repo path is the
// canonical chenhg5/tape; if/when the project ever migrates we'll
// add a constant indirection here.
func newGitHubHost() Host {
	const repo = "chenhg5/tape"
	return Host{
		Name:        "github",
		DisplayName: "GitHub",
		pingURL:     "https://api.github.com/repos/" + repo,
		apiLatest:   "https://api.github.com/repos/" + repo + "/releases/latest",
		apiList:     "https://api.github.com/repos/" + repo + "/releases?per_page=5",
		userAgent:   "tape-cli",
		accept:      "application/vnd.github+json",
		parser:      githubParseOne,
		listParser:  githubParseList,
	}
}

// githubReleaseWire is the wire shape; field set deliberately
// matches the canonical Release plus omits everything else.
type githubReleaseWire struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func githubParseOne(body []byte) (*Release, error) {
	var w githubReleaseWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	return &Release{
		TagName: w.TagName, Name: w.Name, HTMLURL: w.HTMLURL,
		Prerelease: w.Prerelease, PublishedAt: w.PublishedAt,
	}, nil
}

func githubParseList(body []byte) ([]Release, error) {
	var ws []githubReleaseWire
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, err
	}
	out := make([]Release, len(ws))
	for i, w := range ws {
		out[i] = Release{
			TagName: w.TagName, Name: w.Name, HTMLURL: w.HTMLURL,
			Prerelease: w.Prerelease, PublishedAt: w.PublishedAt,
		}
	}
	return out, nil
}

// newGiteeHost returns the Gitee mirror. Repo path is cg33/tape
// per the user-supplied SSH URL — Gitee owner names are independent
// of GitHub's, no automatic mapping is possible.
//
// Gitee API v5 is *mostly* GitHub-compatible at this endpoint, with
// one significant exception: there's no `published_at` field on
// /releases; we synthesize PublishedAt from `created_at` so the
// rest of tape doesn't need to know which mirror it came from.
// html_url is also synthesized from the repo + tag because the API
// doesn't return one.
func newGiteeHost() Host {
	const owner = "cg33"
	const repo = "tape"
	htmlBase := "https://gitee.com/" + owner + "/" + repo
	return Host{
		Name:        "gitee",
		DisplayName: "Gitee",
		// Repo-level GET is cheap, public, and reliable, and gives
		// us a 200 even before any releases are published. Same
		// shape as GitHub's /repos/owner/repo, but Gitee's URL
		// scheme is `/api/v5/repos/...`.
		pingURL:    "https://gitee.com/api/v5/repos/" + owner + "/" + repo,
		apiLatest:  "https://gitee.com/api/v5/repos/" + owner + "/" + repo + "/releases/latest",
		apiList:    "https://gitee.com/api/v5/repos/" + owner + "/" + repo + "/releases?page=1&per_page=5&direction=desc",
		userAgent:  "tape-cli",
		accept:     "application/json",
		parser:     giteeParseOne(htmlBase),
		listParser: giteeParseList(htmlBase),
	}
}

// giteeReleaseWire is the Gitee wire shape. Fields not present on
// GitHub's wire schema: CreatedAt (we map to PublishedAt).
type giteeReleaseWire struct {
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Prerelease bool      `json:"prerelease"`
	CreatedAt  time.Time `json:"created_at"`
}

func giteeParseOne(htmlBase string) func([]byte) (*Release, error) {
	return func(body []byte) (*Release, error) {
		var w giteeReleaseWire
		if err := json.Unmarshal(body, &w); err != nil {
			return nil, err
		}
		return &Release{
			TagName: w.TagName, Name: w.Name,
			HTMLURL:     htmlBase + "/releases/tag/" + w.TagName,
			Prerelease:  w.Prerelease,
			PublishedAt: w.CreatedAt,
		}, nil
	}
}

func giteeParseList(htmlBase string) func([]byte) ([]Release, error) {
	return func(body []byte) ([]Release, error) {
		var ws []giteeReleaseWire
		if err := json.Unmarshal(body, &ws); err != nil {
			return nil, err
		}
		out := make([]Release, len(ws))
		for i, w := range ws {
			out[i] = Release{
				TagName: w.TagName, Name: w.Name,
				HTMLURL:     htmlBase + "/releases/tag/" + w.TagName,
				Prerelease:  w.Prerelease,
				PublishedAt: w.CreatedAt,
			}
		}
		return out, nil
	}
}
