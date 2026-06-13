package cli

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/chenhg5/tape/internal/config"
)

// newConfigCmd wires `tape config {path,get,set,unset,list}`. Tape's
// config surface is intentionally tiny — defaults for the existing
// CLI flags only — so we expose CRUD over a flat dot-keyed schema
// instead of inventing a DSL.
//
// The flag-binding side of config (where ls/search/export actually
// pull their defaults from this file) lives in App.loadDefaults,
// called once per invocation. Putting *that* code here would tangle
// CLI commands with the config sub-tree; keeping it on App means
// adding a new flag-with-default takes one line in one place.
func newConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read or modify tape's user configuration (~/.tape/config.json)",
		Long: `Tape stores user preferences as a single JSON file at
~/.tape/config.json. The file is human-editable — you can open it
in your editor — but for scripted use the subcommands below offer
a safer API.

Keys are dot-paths into the schema. Run 'tape config list' to see
every known key with its current value, or 'tape schema config' for
machine-readable docs.

Today the only thing config controls is per-command defaults: if
you always pass '--exclude-agent cursor' to tape ls, set it once
with 'tape config set defaults.exclude_agents cursor' and stop
typing it. CLI flags still win when they're explicitly passed.

Privacy note: nothing in here is ever sent anywhere; tape never
phones home.`,
	}
	cmd.AddCommand(
		newConfigPathCmd(app),
		newConfigListCmd(app),
		newConfigGetCmd(app),
		newConfigSetCmd(app),
		newConfigUnsetCmd(app),
	)
	return cmd
}

func newConfigPathCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the absolute path of tape's config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.Path(app.home)
			if app.useJSON() {
				return emitJSON(map[string]any{"path": path})
			}
			fmt.Println(path)
			return nil
		},
	}
}

func newConfigListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show every known config key with its current value",
		Long: `Lists every config key recognized by this build, with the value
currently set in ~/.tape/config.json. Unset keys show their zero
value ("", 0, []) so you can tell at a glance which knobs you've
opted into.

For agents, prefer --json — the rendered output includes the
"is_set" boolean and is otherwise schema-stable.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(app.home)
			if err != nil {
				return err
			}
			keys := config.Keys()
			if app.useJSON() {
				entries := make([]map[string]any, 0, len(keys))
				for _, k := range keys {
					v, set, err := config.Get(cfg, k)
					if err != nil {
						return err
					}
					entries = append(entries, map[string]any{
						"key": k, "value": v, "is_set": set,
					})
				}
				return emitJSON(map[string]any{
					"path": config.Path(app.home), "entries": entries,
				})
			}
			app.lead()
			fmt.Printf("  %s %s\n", app.gray("file"), config.Path(app.home))
			fmt.Println()
			for _, k := range keys {
				v, set, err := config.Get(cfg, k)
				if err != nil {
					return err
				}
				mark := app.gray("·")
				if set {
					mark = app.green("✓")
				}
				fmt.Printf("  %s %s = %s\n", mark, app.bold(k), formatValue(v))
			}
			return nil
		},
	}
}

func newConfigGetCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print the current value of one config key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(app.home)
			if err != nil {
				return err
			}
			v, set, err := config.Get(cfg, args[0])
			if err != nil {
				return err
			}
			if app.useJSON() {
				return emitJSON(map[string]any{
					"key": args[0], "value": v, "is_set": set,
				})
			}
			fmt.Println(formatValue(v))
			return nil
		},
	}
}

func newConfigSetCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write a config key (values for slices are comma-separated)",
		Long: `Sets a config key in ~/.tape/config.json. For slice-valued keys
(exclude_agents, exclude_dirs, exclude_hosts) the value is parsed
as comma- or whitespace-separated entries; for integers any base-10
integer; for strings the raw argument.

The change is applied atomically (temp file + rename) so editors
that have the file open won't see a half-written intermediate
state.`,
		Example: `  tape config set defaults.exclude_agents cursor,opencode
  tape config set defaults.jobs 4
  tape config set defaults.format zip`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(app.home)
			if err != nil {
				return err
			}
			if err := config.Set(cfg, args[0], args[1]); err != nil {
				return usageErrf("%v", err)
			}
			if err := config.Save(app.home, cfg); err != nil {
				return err
			}
			if !app.useJSON() {
				app.lead()
				fmt.Printf("  %s %s = %s\n", app.green("✓"), app.bold(args[0]), args[1])
			}
			return nil
		},
	}
}

func newConfigUnsetCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Clear a config key (revert to its CLI default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(app.home)
			if err != nil {
				return err
			}
			if err := config.Unset(cfg, args[0]); err != nil {
				return usageErrf("%v", err)
			}
			if err := config.Save(app.home, cfg); err != nil {
				return err
			}
			if !app.useJSON() {
				app.lead()
				fmt.Printf("  %s cleared %s\n", app.green("✓"), app.bold(args[0]))
			}
			return nil
		},
	}
}

// formatValue is a small renderer for the get/list paths. Slices
// print as "[a, b, c]" instead of Go's "[a b c]" because the
// human eye reads comma-separated lists much faster, and that's
// also exactly the input format `config set` accepts — round-trip.
func formatValue(v any) string {
	if v == nil {
		return ""
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		out := make([]string, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, fmt.Sprint(rv.Index(i).Interface()))
		}
		sort.Strings(out)
		return "[" + strings.Join(out, ", ") + "]"
	}
	return fmt.Sprint(v)
}
