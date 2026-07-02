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
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
}

// commandManifest describes one command (and its subtree).
type commandManifest struct {
	Path     string            `json:"path"`
	Use      string            `json:"use"`
	Short    string            `json:"short,omitempty"`
	Aliases  []string          `json:"aliases,omitempty"`
	Flags    []flagManifest    `json:"flags,omitempty"`
	Commands []commandManifest `json:"commands,omitempty"`
}

// commandsManifest is the top-level document.
type commandsManifest struct {
	Version     string            `json:"version"`
	GlobalFlags []flagManifest    `json:"global_flags"`
	Commands    []commandManifest `json:"commands"`
}

var commandsCmd = &cobra.Command{
	Use:   "commands",
	Short: "Dump the full CLI command tree as a machine-readable manifest",
	Long: `Print every command, subcommand, and flag the CLI supports.

With --format json (or yaml) the output is a structured manifest —
the recommended way for an agent or script to discover the CLI's
capabilities in one call:

  crewship commands --format json | jq '.commands[].path'

The default (table) output is an indented human-readable tree.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		manifest := commandsManifest{
			Version:     version,
			GlobalFlags: collectFlags(rootCmd.PersistentFlags()),
			Commands:    collectCommands(rootCmd, ""),
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
// commands and cobra's built-in help/completion plumbing.
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
			Path:     path,
			Use:      c.Use,
			Short:    c.Short,
			Aliases:  c.Aliases,
			Flags:    collectFlags(c.Flags()),
			Commands: collectCommands(c, path),
		})
	}
	return out
}

// collectFlags converts a pflag set into the manifest shape.
func collectFlags(fs *pflag.FlagSet) []flagManifest {
	var out []flagManifest
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		out = append(out, flagManifest{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Usage:     f.Usage,
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
