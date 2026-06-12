package sqlitefts

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"OAuth2 token", []string{"oauth2", "token"}},
		{"会话备份", []string{"会话", "话备", "备份"}},
		{"用OAuth2做认证", []string{"用", "oauth2", "做认", "认证"}},
		{"a", []string{"a"}},
		{"赞", []string{"赞"}},
		{"", nil},
		{"!!!", nil},
		{"snake_case ok", []string{"snake_case", "ok"}},
	}
	for _, c := range cases {
		got := Tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildMatch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"oauth2", `"oauth2"*`},
		{"会话备份", `"会话 话备 备份"`},
		{"为什么不用 OAuth2", `"为什 什么 么不 不用" "oauth2"*`},
	}
	for _, c := range cases {
		if got := buildMatch(c.in); got != c.want {
			t.Errorf("buildMatch(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
