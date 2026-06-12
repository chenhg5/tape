package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestPromptChoice(t *testing.T) {
	items := []string{"alpha", "beta", "gamma"}
	for _, tc := range []struct {
		name    string
		input   string
		want    int
		wantErr error
		errMsg  string
	}{
		{"pick first", "1\n", 0, nil, ""},
		{"pick middle", "2\n", 1, nil, ""},
		{"pick last", "3\n", 2, nil, ""},
		{"empty = default first", "\n", 0, nil, ""},
		{"q aborts", "q\n", 0, errPromptAborted, ""},
		{"Q aborts", "Q\n", 0, errPromptAborted, ""},
		{"out of range high", "9\n", 0, nil, "invalid selection"},
		{"out of range low", "0\n", 0, nil, "invalid selection"},
		{"non-numeric", "alpha\n", 0, nil, "invalid selection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{}
			got, err := promptChoiceR(app, strings.NewReader(tc.input), "pick:", items)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err=%v want %v", err, tc.wantErr)
				}
			case tc.errMsg != "":
				if err == nil || !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("want err containing %q, got %v", tc.errMsg, err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if got != tc.want {
					t.Fatalf("got %d want %d", got, tc.want)
				}
			}
		})
	}
}

// Empty menu should error rather than panic on items[0].
func TestPromptChoiceEmpty(t *testing.T) {
	_, err := promptChoiceR(&App{}, strings.NewReader("\n"), "pick:", nil)
	if err == nil || !strings.Contains(err.Error(), "nothing to choose") {
		t.Fatalf("want empty-menu error, got %v", err)
	}
}
