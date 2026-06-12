package redact

import (
	"strings"
	"testing"
)

func TestScanAndApply(t *testing.T) {
	data := []byte(`line one
export GITHUB_TOKEN=ghp_aB3dE6gH9jK2mN5pQ8sT1vW4yZ7cF0iL3oR6
aws AKIAIOSFODNN7EXAMPLE ok
"api_key": "super-secret-value-123456"
clean line about oauth2 design`)

	fs := Scan("x/session.json", data)
	rules := map[string]bool{}
	for _, f := range fs {
		rules[f.Rule] = true
		if strings.Contains(f.Preview, "ghp_aB3dE6gH9jK2mN5pQ8sT1vW4yZ7cF0iL3oR6") {
			t.Errorf("preview must be masked, got %q", f.Preview)
		}
	}
	for _, want := range []string{"github-token", "aws-access-key", "generic-credential"} {
		if !rules[want] {
			t.Errorf("missing finding for rule %s (got %v)", want, rules)
		}
	}
	for _, f := range fs {
		if f.Rule == "github-token" && f.Line != 2 {
			t.Errorf("github token line = %d, want 2", f.Line)
		}
	}

	red := Apply(data)
	for _, leaked := range []string{"ghp_aB3dE6gH9jK2mN5pQ8sT1vW4yZ7cF0iL3oR6", "AKIAIOSFODNN7EXAMPLE", "super-secret-value-123456"} {
		if strings.Contains(string(red), leaked) {
			t.Errorf("Apply left secret %q in output", leaked)
		}
	}
	if !strings.Contains(string(red), "[REDACTED:aws-access-key]") {
		t.Errorf("expected redaction marker, got: %s", red)
	}
	if !strings.Contains(string(red), "clean line about oauth2 design") {
		t.Error("clean content must survive redaction")
	}
}

func TestEveryRuleFires(t *testing.T) {
	samples := map[string]string{
		"aws-access-key":     "AKIAIOSFODNN7EXAMPLE",
		"github-token":       "ghp_aB3dE6gH9jK2mN5pQ8sT1vW4yZ7cF0iL3oR6",
		"anthropic-key":      "sk-ant-api03-abcdefghijklmnopqrstuv",
		"openai-key":         "sk-proj-abcdefghijklmnopqrstuvwxyz123456",
		"google-api-key":     "AIzaSyA1234567890abcdefghijklmnopqrstuv"[:39],
		"slack-token":        "xoxb-1234567890-abcdefghij",
		"private-key-block":  "-----BEGIN OPENSSH PRIVATE KEY-----",
		"jwt":                "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJVadQssw5c",
		"bearer-token":       "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789",
		"generic-credential": `password = "hunter2hunter2hunter2"`,
	}
	for rule, sample := range samples {
		found := false
		for _, f := range Scan("p", []byte("context "+sample+" trailing")) {
			if f.Rule == rule {
				found = true
			}
		}
		if !found {
			t.Errorf("rule %s did not fire on %q", rule, sample)
		}
	}
}

func TestPlaceholdersNotFlagged(t *testing.T) {
	// common placeholder shapes that show up in docs and fixtures must not
	// trip the scanner (false-positive control)
	clean := []string{
		"bot_token = \"xoxb-...\"",
		"xoxb-test",
		"use sk-yourkey here", // too short for openai rule
		"AKIA1234",            // too short
	}
	for _, s := range clean {
		if fs := Scan("p", []byte(s)); len(fs) != 0 {
			t.Errorf("placeholder %q flagged: %+v", s, fs)
		}
	}
}

func TestApplyKeepLength(t *testing.T) {
	in := []byte(`before AKIAIOSFODNN7EXAMPLE after`)
	out := ApplyKeepLength(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(out))
	}
	if strings.Contains(string(out), "AKIAIOSFODNN7EXAMPLE") {
		t.Error("secret survived")
	}
	if !strings.HasPrefix(string(out), "before ") || !strings.HasSuffix(string(out), " after") {
		t.Errorf("surroundings damaged: %q", out)
	}
	if strings.Contains(string(in), "xxxx") {
		t.Error("input mutated")
	}
}

func TestScanClean(t *testing.T) {
	if fs := Scan("p", []byte("普通的中文讨论, nothing secret here")); len(fs) != 0 {
		t.Errorf("false positives: %+v", fs)
	}
}
