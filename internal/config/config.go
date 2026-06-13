// Package config persists tape's user preferences as a single JSON
// file at ~/.tape/config.json. We deliberately reach for a tiny
// hand-rolled schema (instead of viper or cobra-config) so:
//
//   - the file is human-editable and human-readable; users open it,
//     tweak a key, save it, no surprises;
//   - the dependency footprint stays small (encoding/json is enough);
//   - the schema is checked in source — easy to grep for, easy to
//     migrate; no string-keyed magic spread across files.
//
// What goes in here is "things the user wants to repeat on every
// invocation": default exclude lists, default --jobs, default export
// codec. What *doesn't* go in here: agent-discovery paths, archive
// directory location, anything tape can figure out on its own. The
// rule of thumb: if a key isn't already a CLI flag, it doesn't
// belong in config.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// FileName is the basename inside $TAPE_HOME. Stable so users can
// share .tape directories across machines without renaming.
const FileName = "config.json"

// Defaults holds the per-command defaults. Add a field here and you
// get config get/set support for free; the CLI binds it explicitly
// to one of its flags by reading via the Get helper.
//
// JSON tags use snake_case to match shell + tape convention. omitempty
// keeps the on-disk file slim — unset keys don't appear at all,
// which makes diffs friendly and makes "is the user using this?"
// trivially answerable by inspecting the file.
type Defaults struct {
	ExcludeAgents  []string `json:"exclude_agents,omitempty"`
	ExcludeDirs    []string `json:"exclude_dirs,omitempty"`
	ExcludeHosts   []string `json:"exclude_hosts,omitempty"`
	Jobs           int      `json:"jobs,omitempty"`
	Format         string   `json:"format,omitempty"`
	Compress       string   `json:"compress,omitempty"`
}

// Config is the top-level on-disk shape. Defaults is the only group
// today; adding new groups (e.g. UpdateCheck, Sync) is a non-
// breaking change because every field is omitempty.
type Config struct {
	Defaults Defaults `json:"defaults,omitempty"`
}

// Path returns the absolute config path for a given tape home.
func Path(home string) string { return filepath.Join(home, FileName) }

// Load reads the config file. Missing file is not an error — it
// just yields a zero Config — so CLI flag-binding can call this
// every invocation without conditional branching.
func Load(home string) (*Config, error) {
	f, err := os.Open(Path(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var c Config
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(home), err)
	}
	return &c, nil
}

// Save writes the config file with pretty-printed JSON and a
// trailing newline. We create the directory + 0700 it if missing
// (so a brand-new install can `tape config set` before any sync).
//
// Atomic via temp file + rename; the user might have it open in an
// editor when we write.
func Save(home string, c *Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := Path(home)
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get reads a single key in dot notation ("defaults.jobs",
// "defaults.exclude_agents") and returns its current value as a
// generic any plus a "set?" boolean. The unset boolean lets callers
// distinguish "user explicitly set this to 0 / ''" from "default".
//
// We use reflection over the struct so adding a new field gets
// dot-key support for free (no switch to update). The cost is one
// reflection walk per call, which is fine for an interactive
// command.
func Get(c *Config, dotKey string) (any, bool, error) {
	v, ok, err := navigate(reflect.ValueOf(c).Elem(), dotKey)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return v.Interface(), !v.IsZero(), nil
}

// Set assigns dotKey to value. value is the raw string the user
// typed; we parse it into the field's Go type (int, string, []string
// with CSV/space splitting). Returns an error for unknown keys and
// for parse failures, leaving the config untouched.
func Set(c *Config, dotKey, value string) error {
	v, ok, err := navigate(reflect.ValueOf(c).Elem(), dotKey)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown key %q (try `tape config list` to see all keys)", dotKey)
	}
	if !v.CanSet() {
		return fmt.Errorf("key %q is not settable", dotKey)
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%q: not an integer", value)
		}
		v.SetInt(n)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("slice of %s is not supported", v.Type().Elem())
		}
		parts := splitCSV(value)
		v.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("kind %s is not supported", v.Kind())
	}
	return nil
}

// Unset clears a key by zero-valuing it. Useful for "I want the
// CLI default back" without dropping the whole file.
func Unset(c *Config, dotKey string) error {
	v, ok, err := navigate(reflect.ValueOf(c).Elem(), dotKey)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown key %q", dotKey)
	}
	if !v.CanSet() {
		return fmt.Errorf("key %q is not settable", dotKey)
	}
	v.Set(reflect.Zero(v.Type()))
	return nil
}

// Keys returns every dot-key the schema knows about, in deterministic
// alpha order. Used by `tape config list` to render unset keys with
// their default value placeholder.
func Keys() []string {
	var out []string
	walk(reflect.TypeOf(Config{}), "", &out)
	sort.Strings(out)
	return out
}

func walk(t reflect.Type, prefix string, out *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name := strings.TrimSuffix(tag, ",omitempty")
		if name == "-" || name == "" {
			continue
		}
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct {
			walk(f.Type, full, out)
			continue
		}
		*out = append(*out, full)
	}
}

// navigate walks the value tree for a dot key, returning the leaf
// reflect.Value. Returns (zero, false, nil) when the key is
// syntactically valid but unknown; returns an error only on a real
// schema mismatch (which shouldn't happen because we constrain
// callers to Keys()).
func navigate(root reflect.Value, dotKey string) (reflect.Value, bool, error) {
	parts := strings.Split(dotKey, ".")
	cur := root
	for _, p := range parts {
		if cur.Kind() != reflect.Struct {
			return reflect.Value{}, false, fmt.Errorf("cannot descend %s under non-struct", p)
		}
		var match reflect.Value
		t := cur.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.TrimSuffix(f.Tag.Get("json"), ",omitempty")
			if tag == p {
				match = cur.Field(i)
				break
			}
		}
		if !match.IsValid() {
			return reflect.Value{}, false, nil
		}
		cur = match
	}
	return cur, true, nil
}

// splitCSV is the same flavor of "comma or whitespace + trim empty"
// the CLI uses elsewhere. We re-implement it here so the config
// package stays free of CLI imports.
func splitCSV(s string) []string {
	var out []string
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			out = append(out, raw)
		}
	}
	return out
}
