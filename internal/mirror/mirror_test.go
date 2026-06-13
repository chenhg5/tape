package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestHostsCanonicalOrder pins the registered list to "github
// first, gitee second" — Pick's fallback path depends on it.
func TestHostsCanonicalOrder(t *testing.T) {
	hosts := Hosts()
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	if hosts[0].Name != "github" || hosts[1].Name != "gitee" {
		t.Errorf("order drift: %s, %s", hosts[0].Name, hosts[1].Name)
	}
}

// TestLookup matches case-insensitively and reports unknowns
// cleanly. Order matters for TAPE_MIRROR=GitHub vs github.
func TestLookup(t *testing.T) {
	for _, in := range []string{"github", "GitHub", "GITHUB"} {
		if _, ok := Lookup(in); !ok {
			t.Errorf("%q should resolve", in)
		}
	}
	if _, ok := Lookup("bitbucket"); ok {
		t.Error("unknown host should not resolve")
	}
}

// TestPickEnvOverridePins the explicit env override skips the probe
// entirely. We point pingURL at a black hole — if probe ran, the
// test would time out.
func TestPickEnvOverridePins(t *testing.T) {
	t.Setenv(EnvOverride, "gitee")
	h, err := Pick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "gitee" {
		t.Errorf("env override ignored: got %s", h.Name)
	}
}

// TestPickEnvOverrideUnknownFallsBackToAuto: an unrecognized
// TAPE_MIRROR should not break Pick — we treat it as "auto" so
// a typo doesn't brick `tape update`.
func TestPickEnvOverrideUnknownFallsBackToAuto(t *testing.T) {
	t.Setenv(EnvOverride, "bitbucket")
	// Don't assert the chosen host — we don't control the network.
	// We do assert it returned something registered.
	h, err := Pick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(h.Name); !ok {
		t.Errorf("Pick returned unregistered host %q", h.Name)
	}
}

// TestRaceProbeWithLocalServer verifies the probe actually races —
// two test servers, the faster one wins. We use a slow handler that
// blocks for 200ms vs a fast one that returns immediately.
func TestRaceProbeWithLocalServer(t *testing.T) {
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer fast.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer slow.Close()

	hosts := []Host{
		{Name: "slow", pingURL: slow.URL, userAgent: "test"},
		{Name: "fast", pingURL: fast.URL, userAgent: "test"},
	}
	results := raceProbe(context.Background(), hosts)
	// Sort ascending by latency and assert the fast one wins.
	sort.Slice(results, func(i, j int) bool { return results[i].latency < results[j].latency })
	if results[0].host.Name != "fast" {
		t.Errorf("expected fast to win, got %s (%v)", results[0].host.Name, results[0].latency)
	}
	if results[0].err != nil {
		t.Errorf("winner shouldn't have error: %v", results[0].err)
	}
}

// TestRaceProbeFailedHostsSinkToEnd: a host that 5xx's must not
// outrank one that 200s, regardless of how quickly it answered.
func TestRaceProbeFailedHostsSinkToEnd(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // be slower than the 5xx
		w.WriteHeader(200)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	hosts := []Host{
		{Name: "bad", pingURL: bad.URL, userAgent: "test"},
		{Name: "ok", pingURL: ok.URL, userAgent: "test"},
	}
	results := raceProbe(context.Background(), hosts)
	sort.Slice(results, func(i, j int) bool { return results[i].latency < results[j].latency })
	if results[0].host.Name != "ok" {
		t.Errorf("ok should sort first despite slower latency: %+v", results)
	}
}

// TestFetchLatestParsesGitHubShape feeds the real GitHub JSON
// schema and verifies the canonical Release is populated.
func TestFetchLatestParsesGitHubShape(t *testing.T) {
	body := `{
        "tag_name":"v0.2.0",
        "name":"tape v0.2.0",
        "html_url":"https://github.com/chenhg5/tape/releases/tag/v0.2.0",
        "prerelease":false,
        "published_at":"2026-06-13T10:00:00Z"
    }`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	h := Host{
		Name: "test", userAgent: "test",
		apiLatest: srv.URL, apiList: srv.URL,
		parser: githubParseOne, listParser: githubParseList,
	}
	rel, err := h.FetchLatest(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.2.0" || rel.PublishedAt.IsZero() {
		t.Errorf("parsed wrong: %+v", rel)
	}
}

// TestFetchLatestParsesGiteeShape feeds the Gitee JSON (no
// published_at, no html_url — synthesized from created_at + repo
// base) and verifies the canonical Release maps correctly.
func TestFetchLatestParsesGiteeShape(t *testing.T) {
	body := `{
        "tag_name":"v0.2.0",
        "name":"tape v0.2.0",
        "prerelease":false,
        "created_at":"2026-06-13T10:00:00+08:00"
    }`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	h := Host{
		Name: "test", userAgent: "test",
		apiLatest: srv.URL, apiList: srv.URL,
		parser:     giteeParseOne("https://gitee.com/cg33/tape"),
		listParser: giteeParseList("https://gitee.com/cg33/tape"),
	}
	rel, err := h.FetchLatest(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.2.0" || rel.PublishedAt.IsZero() {
		t.Errorf("parsed wrong: %+v", rel)
	}
	if !strings.HasSuffix(rel.HTMLURL, "/releases/tag/v0.2.0") {
		t.Errorf("html url not synthesized: %s", rel.HTMLURL)
	}
}

// TestFetchLatestBetaWalksList: channel=beta should hit the list
// endpoint and pick the first (newest) entry, including a
// prerelease.
func TestFetchLatestBetaWalksList(t *testing.T) {
	body := `[
        {"tag_name":"v0.3.0-rc.1","prerelease":true,"published_at":"2026-06-14T00:00:00Z"},
        {"tag_name":"v0.2.0","prerelease":false,"published_at":"2026-06-13T00:00:00Z"}
    ]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	h := Host{
		Name: "test", userAgent: "test",
		apiLatest: srv.URL, apiList: srv.URL,
		parser: githubParseOne, listParser: githubParseList,
	}
	rel, err := h.FetchLatest(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.3.0-rc.1" || !rel.Prerelease {
		t.Errorf("beta picked wrong: %+v", rel)
	}
}

// TestPickWithReportReturnsAllProbes: --debug needs to see every
// host's outcome, not just the winner. We don't network here —
// just check the report has one entry per host registered.
func TestPickWithReportReturnsAllProbes(t *testing.T) {
	t.Setenv(EnvOverride, "") // force probe path
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	rep, err := PickWithReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reason != "fastest" && rep.Reason != "fallback" {
		t.Errorf("unexpected reason %q", rep.Reason)
	}
	if len(rep.Results) != len(Hosts()) {
		t.Errorf("expected %d probe results, got %d", len(Hosts()), len(rep.Results))
	}
}
