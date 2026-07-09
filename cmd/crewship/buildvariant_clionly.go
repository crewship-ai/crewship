//go:build clionly

package main

// cliOnlyVariant is true in the CLI-only build (shipped as crewship-cli).
// See buildvariant_full.go for why self-update needs this.
const cliOnlyVariant = true
