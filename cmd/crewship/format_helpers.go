package main

import (
	"fmt"

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
	// Some older/local commands own a string --format flag. On an error path
	// exitWithError receives the resolved Cobra command, not the command's
	// successful formatter, so preserve the explicitly supplied local value
	// here. Looking only at flagFormat loses it when the child shadows the
	// persistent root flag.
	//
	// A command that shadows "format" also loses the root flag's `-f`
	// shorthand — pflag merges by name, so the persistent flag is never added
	// and `-f json` fails with `unknown shorthand flag` rather than falling
	// back (#2086). New commands must not do it; the ones that did are being
	// unwound.
	if f := cmd.Flags().Lookup("format"); f != nil && f.Changed && f.Value.String() != "" {
		return f.Value.String()
	}
	// --output-format is the deprecated, differently-NAMED alias left behind
	// when such a shadow is removed: it keeps the old shorthand working
	// without stealing "format" back and re-breaking `-f`.
	if f := cmd.Flags().Lookup("output-format"); f != nil && f.Changed && f.Value.String() != "" {
		return f.Value.String()
	}
	return cli.ResolveFormat(flagFormat, cliCfg)
}

// resolvedFormatter is resolvedFormat wrapped in the shared Formatter, for
// commands that render whole documents (lists, details) rather than a
// bespoke JSON schema.
func resolvedFormatter(cmd *cobra.Command) *cli.Formatter {
	return cli.NewFormatter(resolvedFormat(cmd))
}

// emptyListNote renders the "there is nothing here" case of a list command.
//
// The shape it replaces was everywhere:
//
//	if len(rows) == 0 {
//	    fmt.Println("No skills assigned to this agent.")
//	    return nil
//	}
//	f := newFormatter()
//	…
//
// which reads as a courtesy and is a contract break: the early return happens
// BEFORE the format is resolved, so `-f json` on an empty result gets an
// English sentence and exit 0. Worse, it fails only when the list is empty —
// so it passes every test written against seeded data and breaks on the fresh
// install (#2086).
//
// `empty` is the (nil or empty) typed slice, which Formatter renders as `[]`;
// `note` is the sentence, which only a human ever sees.
func emptyListNote(cmd *cobra.Command, empty any, note string) error {
	return resolvedFormatter(cmd).AutoHuman(empty, func() { fmt.Println(note) })
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
