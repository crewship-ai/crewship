//go:build race

package database

// Race instrumentation changes wall time; production budgets run without it.
const raceEnabled = true
