// Command tape is the composition root: it knows every concrete adapter
// and wires them into the CLI. Nothing else in the codebase does.
package main

import (
	"os"

	"github.com/chenhg5/tape/internal/cli"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/claudecode"
	"github.com/chenhg5/tape/internal/source/codex"
	"github.com/chenhg5/tape/internal/source/cursor"
)

var version = "0.1.0-dev"

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(cli.ExitError)
	}
	app := &cli.App{
		Version: version,
		Sources: []ports.Source{
			claudecode.New(home),
			codex.New(home),
			cursor.New(home),
		},
	}
	os.Exit(cli.Execute(app))
}
