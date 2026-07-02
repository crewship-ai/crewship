package main

import (
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// resolvedFormat returns the effective output format for cmd.
//
// A handful of commands grew a per-command --json bool before the global
// --format/-f flag existed. That bool stays as a silent backwards-compat
// alias (scripts depend on it), but the single source of truth is the
// global format: --json folds into "json", everything else resolves via
// flag > config > "table". New commands must NOT add local --json flags —
// route output through resolvedFormat / the shared Formatter instead.
func resolvedFormat(cmd *cobra.Command) string {
	if b, err := cmd.Flags().GetBool("json"); err == nil && b {
		return "json"
	}
	return cli.ResolveFormat(flagFormat, cliCfg)
}

// resolvedFormatter is resolvedFormat wrapped in the shared Formatter, for
// commands that render whole documents (lists, details) rather than a
// bespoke JSON schema.
func resolvedFormatter(cmd *cobra.Command) *cli.Formatter {
	return cli.NewFormatter(resolvedFormat(cmd))
}

// skipConfirm reports whether the operator pre-confirmed a destructive
// command. --yes is the CLI-wide convention (see confirmAction); a few
// commands grew --force first and keep it as an alias. Only commands whose
// --force means "skip the confirmation prompt" route through here — a
// --force that bypasses a *safety guard* (e.g. memory restore's canonical-
// path confinement) must NOT be conflated with mere pre-confirmation.
func skipConfirm(cmd *cobra.Command) bool {
	if b, err := cmd.Flags().GetBool("yes"); err == nil && b {
		return true
	}
	b, err := cmd.Flags().GetBool("force")
	return err == nil && b
}
