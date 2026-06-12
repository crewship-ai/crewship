package devcontainer

import (
	"testing"
)

func TestFilterRuntimes_EmptyQueryReturnsCopy(t *testing.T) {
	t.Parallel()

	entries := []RuntimeCatalogEntry{
		{Name: "Node.js", Tool: "node", Category: "languages"},
		{Name: "Terraform", Tool: "terraform", Category: "cloud"},
	}
	out := FilterRuntimes(entries, "")
	if len(out) != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), len(out))
	}
	// Must be a copy, not an alias.
	out[0].Name = "mutated"
	if entries[0].Name != "Node.js" {
		t.Error("FilterRuntimes returned an aliased slice; mutation leaked into input")
	}
}

func TestFilterRuntimes_Matching(t *testing.T) {
	t.Parallel()

	entries := []RuntimeCatalogEntry{
		{Name: "Node.js", Tool: "node", Description: "JavaScript runtime.", Category: "languages"},
		{Name: "Terraform", Tool: "terraform", Description: "Infrastructure as code.", Category: "cloud"},
		{Name: "PostgreSQL", Tool: "postgres", Description: "Database server.", Category: "databases"},
	}

	tests := []struct {
		query string
		want  []string // expected Tool values, in order
	}{
		{"NODE", []string{"node"}},                       // case-insensitive name match
		{"terraform", []string{"terraform"}},             // tool match
		{"database", []string{"postgres"}},               // description match
		{"cloud", []string{"terraform"}},                 // category match
		{"  node  ", []string{"node"}},                   // query is trimmed
		{"no-such-runtime-xyz", nil},                     // no match
		{"a", []string{"node", "terraform", "postgres"}}, // common letter matches all
	}
	for _, tt := range tests {
		got := FilterRuntimes(entries, tt.query)
		if len(got) != len(tt.want) {
			t.Errorf("FilterRuntimes(%q): got %d results, want %d", tt.query, len(got), len(tt.want))
			continue
		}
		for i, w := range tt.want {
			if got[i].Tool != w {
				t.Errorf("FilterRuntimes(%q)[%d].Tool = %q, want %q", tt.query, i, got[i].Tool, w)
			}
		}
	}
}

func TestCategorizeRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool string
		want string
	}{
		{"node", "languages"},
		{"Python", "languages"}, // case folded
		{"gleam", "languages"},
		{"postgres", "databases"},
		{"duckdb", "databases"},
		{"terraform", "cloud"},
		{"oci-cli", "cloud"},
		{"awscli", "cloud"},
		{"aws-sam-cli", "cloud"},    // heuristic: contains "aws"
		{"gcloud-extra", "cloud"},   // heuristic: contains "gcloud"
		{"my-azure-thing", "cloud"}, // heuristic: contains "azure"
		{"lazygit", "tools"},
		{"direnv", "tools"},
	}
	for _, tt := range tests {
		if got := CategorizeRuntime(tt.tool); got != tt.want {
			t.Errorf("CategorizeRuntime(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestRuntimeIcon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool     string
		category string
		want     string
	}{
		{"node", "", "hexagon"},
		{"NodeJS", "", "hexagon"}, // case folded
		{"go", "", "arrow-right"},
		{"golang", "", "arrow-right"},
		{"python", "", "code"},
		{"rust", "", "cog"},
		{"ruby", "", "gem"},
		{"crystal", "", "gem"},
		{"java", "", "coffee"},
		{"kubectl", "", "ship"},
		{"helm", "", "ship"},
		{"terraform", "", "blocks"},
		{"pulumi", "", "blocks"},
		{"gh", "", "github"},
		{"github", "", "github"},
		{"docker", "", "container"},
		// Category fallbacks for unknown tools.
		{"elixir", "languages", "code"},
		{"redis", "databases", "database"},
		{"flyctl", "cloud", "cloud"},
		{"some-tool", "tools", "wrench"},
		{"some-tool", "", "wrench"},
	}
	for _, tt := range tests {
		if got := RuntimeIcon(tt.tool, tt.category); got != tt.want {
			t.Errorf("RuntimeIcon(%q, %q) = %q, want %q", tt.tool, tt.category, got, tt.want)
		}
	}
}
