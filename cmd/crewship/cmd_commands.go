package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
)

// commandsCmd dumps the CLI's own command tree as a machine-readable
// manifest — the self-description an agent reads ONCE to learn the whole
// surface (names, arg shapes, flags) instead of scraping `--help` page by
// page. The manifest is generated from the live cobra tree, so it can
// never drift from the binary it ships in.

// flagManifest describes one flag in the commands manifest.
type flagManifest struct {
	Name       string `json:"name" yaml:"name"`
	Shorthand  string `json:"shorthand,omitempty" yaml:"shorthand,omitempty"`
	Type       string `json:"type" yaml:"type"`
	Default    string `json:"default,omitempty" yaml:"default,omitempty"`
	Usage      string `json:"usage" yaml:"usage"`
	Required   bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Deprecated string `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
}

// commandManifest describes one command (and its subtree).
type commandManifest struct {
	Runnable       bool              `json:"runnable" yaml:"runnable"`
	Example        string            `json:"example,omitempty" yaml:"example,omitempty"`
	InheritedFlags []flagManifest    `json:"inherited_flags,omitempty" yaml:"inherited_flags,omitempty"`
	Path           string            `json:"path" yaml:"path"`
	Use            string            `json:"use" yaml:"use"`
	Short          string            `json:"short,omitempty" yaml:"short,omitempty"`
	Aliases        []string          `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	Flags          []flagManifest    `json:"flags,omitempty" yaml:"flags,omitempty"`
	Commands       []commandManifest `json:"commands,omitempty" yaml:"commands,omitempty"`
}

// commandsManifest is the top-level document.
type commandsManifest struct {
	Version     string            `json:"version" yaml:"version"`
	GlobalFlags []flagManifest    `json:"global_flags" yaml:"global_flags"`
	Commands    []commandManifest `json:"commands" yaml:"commands"`
}

var commandsCmd = &cobra.Command{
	Use:   "commands [command path...]",
	Short: "Dump the full CLI command tree as a machine-readable manifest",
	Long: `Print every command, subcommand, and flag the CLI supports.

With --format json (or yaml) the output is a structured manifest —
the recommended way for an agent or script to discover the CLI's
capabilities in one call:

  crewship commands --format json | jq '.commands[].path'

Pass a canonical command path to focus on one subtree:

  crewship commands routine run --format json

The manifest includes runnable status, examples, inherited flags, and individually
required flags. Conditional requirements remain documented in command help.
The default (table) output is an indented human-readable tree.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		manifest := commandsManifest{
			Version:     version,
			GlobalFlags: collectFlags(rootCmd.PersistentFlags()),
			Commands:    collectCommands(rootCmd, ""),
		}
		if len(args) > 0 {
			path := strings.Join(strings.Fields(strings.Join(args, " ")), " ")
			selected := findCommandManifest(manifest.Commands, path)
			if selected == nil {
				return cli.NotFoundf("command not found: %s (run 'crewship commands')", path)
			}
			manifest.Commands = []commandManifest{*selected}
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return f.Auto(manifest, nil, nil)
		case "quiet":
			// Script-friendly: one command path per line, nothing else.
			printCommandPaths(manifest.Commands)
			return nil
		default:
			printCommandTree(manifest.Commands, 0)
			fmt.Printf("\n%sFull machine-readable manifest: crewship commands --format json%s\n", cli.Dim, cli.Reset)
			return nil
		}
	},
}

// collectCommands walks the cobra tree depth-first, skipping hidden
// commands and cobra's auto-generated `help` command. `completion` is
// kept — it's a real user-facing command (docs/cli/completion.mdx), not
// plumbing an agent should be blind to.
func collectCommands(parent *cobra.Command, prefix string) []commandManifest {
	children := parent.Commands()
	out := make([]commandManifest, 0, len(children))
	for _, c := range children {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		path := c.Name()
		if prefix != "" {
			path = prefix + " " + c.Name()
		}
		out = append(out, commandManifest{
			Runnable:       c.Runnable(),
			Example:        c.Example,
			InheritedFlags: collectInheritedFlags(c),
			Path:           path,
			Use:            c.Use,
			Short:          c.Short,
			Aliases:        c.Aliases,
			Flags:          collectFlags(c.LocalFlags()),
			Commands:       collectCommands(c, path),
		})
	}
	return out
}

// Global flags already appear once at the document root. Repeating them for
// every command needlessly consumes the agent's context window.
func collectInheritedFlags(cmd *cobra.Command) []flagManifest {
	local := pflag.NewFlagSet("inherited", pflag.ContinueOnError)
	global := cmd.Root().PersistentFlags()
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if global.Lookup(flag.Name) != flag {
			local.AddFlag(flag)
		}
	})
	return collectFlags(local)
}

// collectFlags converts a pflag set into the manifest shape.
func collectFlags(fs *pflag.FlagSet) []flagManifest {
	var out []flagManifest
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		out = append(out, flagManifest{
			Name:       f.Name,
			Shorthand:  f.Shorthand,
			Type:       f.Value.Type(),
			Default:    f.DefValue,
			Usage:      f.Usage,
			Required:   len(f.Annotations[cobra.BashCompOneRequiredFlag]) > 0,
			Deprecated: f.Deprecated,
		})
	})
	return out
}

// printCommandPaths emits every command path, one per line — quiet mode.
func printCommandPaths(cmds []commandManifest) {
	for _, c := range cmds {
		fmt.Println(c.Path)
		printCommandPaths(c.Commands)
	}
}

// printCommandTree renders the human view: an indented name + short tree.
func printCommandTree(cmds []commandManifest, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, c := range cmds {
		name := c.Path
		if idx := strings.LastIndex(c.Path, " "); idx >= 0 {
			name = c.Path[idx+1:]
		}
		fmt.Printf("%s%s%-24s%s %s\n", indent, cli.Bold, name, cli.Reset, c.Short)
		printCommandTree(c.Commands, depth+1)
	}
}

func init() {
	rootCmd.AddCommand(commandsCmd)
}

// findCommandManifest accepts canonical paths only, so the returned path can be
// reused verbatim. This lookup never executes a command or contacts a server.
func findCommandManifest(commands []commandManifest, path string) *commandManifest {
	for i := range commands {
		if commands[i].Path == path {
			return &commands[i]
		}
		if found := findCommandManifest(commands[i].Commands, path); found != nil {
			return found
		}
	}
	return nil
}
