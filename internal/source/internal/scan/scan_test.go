package scan

import (
	"regexp"
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("short string changed: %q", got)
	}
	if got := Truncate("hello world", 5); got != "hello…" {
		t.Errorf("got %q", got)
	}
	// must not split a multi-byte rune
	s := "abc中文def"
	got := Truncate(s, 4) // byte 4 is inside 中
	if !strings.HasPrefix(got, "abc") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("rune split: %q", got)
	}
	for cut := 0; cut < len(s); cut++ {
		if out := Truncate(s, cut); !utf8Valid(out) {
			t.Errorf("Truncate(%q,%d) produced invalid UTF-8: %q", s, cut, out)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello\nworld", "hello"},
		{"\n\n  second  \nthird", "second"},
		{"", ""},
		{"   \n\t\n", ""},
		{strings.Repeat("x", 100) + "\ny", strings.Repeat("x", 80) + "…"},
	}
	for _, c := range cases {
		if got := FirstLine(c.in, 80); got != c.want {
			t.Errorf("FirstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := UUIDv4()
		if !uuidRe.MatchString(id) {
			t.Fatalf("malformed uuid: %s", id)
		}
		if id[14] != '4' {
			t.Fatalf("not version 4: %s", id)
		}
		if seen[id] {
			t.Fatalf("duplicate uuid: %s", id)
		}
		seen[id] = true
	}
}

func TestUUIDv7(t *testing.T) {
	a, b := UUIDv7(), UUIDv7()
	for _, id := range []string{a, b} {
		if !uuidRe.MatchString(id) {
			t.Fatalf("malformed uuid: %s", id)
		}
		if id[14] != '7' {
			t.Fatalf("not version 7: %s", id)
		}
	}
	// v7 is time-ordered: ids generated in sequence must not decrease
	if a >= b && a[:13] != b[:13] {
		t.Errorf("v7 ordering violated: %s >= %s", a, b)
	}
}
