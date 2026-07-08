//go:build !clionly

package main

// cliOnlyVariant reports whether this binary is the CLI-only build
// (`-tags clionly`, shipped as crewship-cli). The full build sets it false.
// self-update reads it to pick the matching release archive + Homebrew
// formula so a crewship-cli install never gets silently swapped for the
// ~2× larger full server binary.
const cliOnlyVariant = false
