package main

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/notify"
)

// categoryHelp renders the notification category vocabulary, grouped, for CLI
// help text. Generated from internal/notify.CategoryGroups rather than
// hard-coded: the list was previously spelled out in three separate help
// strings, and taxonomy v2 found all three had drifted from the backend.
func categoryHelp() string {
	var b strings.Builder
	b.WriteString("Categories:\n")
	for _, g := range notify.CategoryGroups {
		b.WriteString("  ")
		b.WriteString(g.Label)
		b.WriteString(": ")
		b.WriteString(strings.Join(g.Categories, ", "))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
