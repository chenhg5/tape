package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// schema lets agents introspect the command tree on demand instead of
// pasting all help text into context (agent-cli-guide principle 7).

type cmdSchema struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Usage       string       `json:"usage"`
	Example     string       `json:"example,omitempty"`
	Flags       []flagSchema `json:"flags,omitempty"`
	Subcommands []cmdSchema  `json:"subcommands,omitempty"`
}

type flagSchema struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

func newSchemaCmd(app *App, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:     "schema [command...]",
		Short:   "Introspect the command tree as JSON (for agents)",
		Example: "  tape schema\n  tape schema export",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := root
			if len(args) > 0 {
				c, _, err := root.Find(args)
				if err != nil {
					return usageErrf("unknown command %v", args)
				}
				target = c
			}
			return emitJSON(describe(target))
		},
	}
}

func describe(c *cobra.Command) cmdSchema {
	s := cmdSchema{
		Name:        c.Name(),
		Description: c.Short,
		Usage:       c.UseLine(),
		Example:     c.Example,
	}
	collect := func(f *pflag.Flag) {
		s.Flags = append(s.Flags, flagSchema{
			Name:        "--" + f.Name,
			Type:        f.Value.Type(),
			Default:     f.DefValue,
			Description: f.Usage,
		})
	}
	c.LocalFlags().VisitAll(collect)
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		s.Subcommands = append(s.Subcommands, describe(sub))
	}
	return s
}
