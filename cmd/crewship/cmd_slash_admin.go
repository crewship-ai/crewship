package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// slashCmd groups admin operations on user-defined slash commands.
//
// The bulk of slash-command surface is the auto-registered subcommands
// themselves (e.g. `crewship review-pr`); this `slash` group is the
// meta surface — list / show / where-from / init.
var slashCmd = &cobra.Command{
	Use:   "slash",
	Short: "Manage user-defined slash commands (~/.crewship/commands/*.md)",
}

var slashListCmd = &cobra.Command{
	Use:   "list",
	Short: "List loaded slash commands",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmds, err := cli.LoadSlashCommands()
		if err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"NAME", "DESCRIPTION", "AGENT", "SOURCE"}
		rows := make([][]string, 0, len(cmds))
		for _, c := range cmds {
			rows = append(rows, []string{c.Name, c.Description, c.Agent, c.Source})
		}
		return f.Auto(cmds, headers, rows)
	},
}

var slashInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold the ~/.crewship/commands directory with a sample",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := cli.DefaultSlashDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		sample := filepath.Join(dir, "review.md")
		if _, err := os.Stat(sample); err == nil {
			fmt.Printf("Sample already exists at %s — leaving it alone\n", sample)
			return nil
		}
		content := `---
name: review
description: Ask the default agent to review a git diff
vars:
  - target
plan: false
---
Review the following ${target} for correctness, security, and style.
Be terse. Lead with the highest-severity issue.

` + "```\n$args\n```\n"
		if err := os.WriteFile(sample, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("Created %s — try: crewship review staged 'changes'\n", sample)
		return nil
	},
}

func init() {
	slashCmd.AddCommand(slashListCmd)
	slashCmd.AddCommand(slashInitCmd)
	rootCmd.AddCommand(slashCmd)
}
