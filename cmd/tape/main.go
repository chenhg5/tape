// Command tape is the composition root: it knows every concrete adapter
// and wires them into the CLI. Nothing else in the codebase does.
package main

import (
	"os"

	"github.com/chenhg5/tape/internal/cli"
	"github.com/chenhg5/tape/internal/core/ports"
	"github.com/chenhg5/tape/internal/source/aider"
	"github.com/chenhg5/tape/internal/source/claudecode"
	"github.com/chenhg5/tape/internal/source/codex"
	"github.com/chenhg5/tape/internal/source/cursor"
	"github.com/chenhg5/tape/internal/source/gemini"
	"github.com/chenhg5/tape/internal/source/iflow"
	"github.com/chenhg5/tape/internal/source/opencode"
	"github.com/chenhg5/tape/internal/source/qwen"
)

var version = "0.1.0-dev"

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(cli.ExitError)
	}
	factory := func(home string) []ports.Source {
		return []ports.Source{
			claudecode.New(home),
			codex.New(home),
			cursor.New(home),
			gemini.New(home),
			qwen.New(home),
			iflow.New(home),
			aider.New(home),
			opencode.New(home),
		}
	}
	app := &cli.App{
		Version:       version,
		Sources:       factory(home),
		SourceFactory: factory,
	}
	os.Exit(cli.Execute(app))
}
