package devcontainer

import "strings"

// RuntimeCatalogEntry describes a mise-managed runtime/tool surfaced in the UI
// picker.
type RuntimeCatalogEntry struct {
	Name           string   `json:"name"`
	Tool           string   `json:"tool"`
	Description    string   `json:"description,omitempty"`
	Category       string   `json:"category"` // "languages", "tools", "cloud", "databases"
	Icon           string   `json:"icon"`
	Versions       []string `json:"versions,omitempty"`
	DefaultVersion string   `json:"default_version,omitempty"`
	Backends       []string `json:"backends,omitempty"` // asdf, cargo, npm, pipx, github
}

// FallbackRuntimeCatalog is the built-in list of popular mise tools. It is
// used only when the dynamic upstream fetch fails (see runtimes_fetcher.go).
var FallbackRuntimeCatalog = []RuntimeCatalogEntry{
	// Languages
	{Name: "Node.js", Tool: "node", Description: "JavaScript runtime.", Category: "languages", Icon: "hexagon", Versions: []string{"22", "20", "18"}, DefaultVersion: "22"},
	{Name: "Python", Tool: "python", Description: "Python interpreter.", Category: "languages", Icon: "code", Versions: []string{"3.13", "3.12", "3.11", "3.10"}, DefaultVersion: "3.12"},
	{Name: "Go", Tool: "go", Description: "Go compiler and toolchain.", Category: "languages", Icon: "arrow-right", Versions: []string{"1.23", "1.22", "1.21"}, DefaultVersion: "1.23"},
	{Name: "Rust", Tool: "rust", Description: "Rust toolchain via rustup.", Category: "languages", Icon: "cog", Versions: []string{"stable", "beta", "nightly"}, DefaultVersion: "stable"},
	{Name: "Ruby", Tool: "ruby", Description: "Ruby interpreter.", Category: "languages", Icon: "gem", Versions: []string{"3.3", "3.2", "3.1"}, DefaultVersion: "3.3"},
	{Name: "Java", Tool: "java", Description: "Java Development Kit.", Category: "languages", Icon: "coffee", Versions: []string{"21", "17", "11"}, DefaultVersion: "21"},
	{Name: "Erlang", Tool: "erlang", Description: "Erlang/OTP runtime.", Category: "languages", Icon: "code", Versions: []string{"27", "26"}, DefaultVersion: "27"},
	{Name: "Elixir", Tool: "elixir", Description: "Elixir language.", Category: "languages", Icon: "droplet", Versions: []string{"1.17", "1.16"}, DefaultVersion: "1.17"},
	{Name: "Deno", Tool: "deno", Description: "Secure JavaScript/TypeScript runtime.", Category: "languages", Icon: "code", Versions: []string{"2", "1"}, DefaultVersion: "2"},
	{Name: "Bun", Tool: "bun", Description: "Fast JavaScript runtime and toolkit.", Category: "languages", Icon: "code", Versions: []string{"1"}, DefaultVersion: "1"},
	{Name: ".NET", Tool: "dotnet", Description: ".NET SDK.", Category: "languages", Icon: "hash", Versions: []string{"8", "7"}, DefaultVersion: "8"},
	{Name: "PHP", Tool: "php", Description: "PHP interpreter.", Category: "languages", Icon: "code", Versions: []string{"8.3", "8.2", "8.1"}, DefaultVersion: "8.3"},
	{Name: "Swift", Tool: "swift", Description: "Swift language toolchain.", Category: "languages", Icon: "feather", Versions: []string{"5.10", "5.9"}, DefaultVersion: "5.10"},
	{Name: "Zig", Tool: "zig", Description: "Zig language toolchain.", Category: "languages", Icon: "zap", Versions: []string{"0.13", "0.12"}, DefaultVersion: "0.13"},
	{Name: "Crystal", Tool: "crystal", Description: "Crystal language.", Category: "languages", Icon: "gem", Versions: []string{"1.13", "1.12"}, DefaultVersion: "1.13"},

	// Cloud / Infra
	{Name: "Terraform", Tool: "terraform", Description: "Infrastructure as code.", Category: "cloud", Icon: "blocks", Versions: []string{"1.9", "1.8"}, DefaultVersion: "1.9"},
	{Name: "kubectl", Tool: "kubectl", Description: "Kubernetes CLI.", Category: "cloud", Icon: "ship", Versions: []string{"1.31", "1.30"}, DefaultVersion: "1.31"},
	{Name: "Helm", Tool: "helm", Description: "Kubernetes package manager.", Category: "cloud", Icon: "ship", Versions: []string{"3"}, DefaultVersion: "3"},
	{Name: "AWS CLI", Tool: "awscli", Description: "Amazon Web Services CLI.", Category: "cloud", Icon: "cloud", Versions: []string{"2"}, DefaultVersion: "2"},
	{Name: "gcloud", Tool: "gcloud", Description: "Google Cloud CLI.", Category: "cloud", Icon: "cloud"},
	{Name: "Pulumi", Tool: "pulumi", Description: "Modern infrastructure as code.", Category: "cloud", Icon: "blocks"},

	// Databases
	{Name: "PostgreSQL", Tool: "postgres", Description: "PostgreSQL database server.", Category: "databases", Icon: "database", Versions: []string{"17", "16", "15"}, DefaultVersion: "16"},
	{Name: "Redis", Tool: "redis", Description: "In-memory data store.", Category: "databases", Icon: "database", Versions: []string{"7"}, DefaultVersion: "7"},
	{Name: "MySQL", Tool: "mysql", Description: "MySQL database server.", Category: "databases", Icon: "database", Versions: []string{"8"}, DefaultVersion: "8"},

	// Tools
	{Name: "GitHub CLI", Tool: "gh", Description: "GitHub command-line tool.", Category: "tools", Icon: "github"},
	{Name: "pnpm", Tool: "pnpm", Description: "Fast, disk-efficient package manager.", Category: "tools", Icon: "package", Versions: []string{"9", "8"}, DefaultVersion: "9"},
	{Name: "Yarn", Tool: "yarn", Description: "JavaScript package manager.", Category: "tools", Icon: "package", Versions: []string{"1", "4"}, DefaultVersion: "4"},
	{Name: "direnv", Tool: "direnv", Description: "Load/unload env based on directory.", Category: "tools", Icon: "wrench"},
	{Name: "lazygit", Tool: "lazygit", Description: "Terminal UI for git.", Category: "tools", Icon: "git-branch"},
	{Name: "act", Tool: "act", Description: "Run GitHub Actions locally.", Category: "tools", Icon: "play"},
}

// FilterRuntimes returns entries whose Name, Tool, Description, or Category
// contain the query string (case-insensitive). An empty query returns a copy
// of the full list.
func FilterRuntimes(entries []RuntimeCatalogEntry, query string) []RuntimeCatalogEntry {
	if query == "" {
		out := make([]RuntimeCatalogEntry, len(entries))
		copy(out, entries)
		return out
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var results []RuntimeCatalogEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Tool), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(e.Category), q) {
			results = append(results, e)
		}
	}
	return results
}

// CategorizeRuntime returns a best-guess category for a mise tool name.
// Used to bucket tools fetched from the upstream registry.
func CategorizeRuntime(tool string) string {
	t := strings.ToLower(tool)
	switch t {
	case "node", "nodejs", "python", "go", "golang", "rust", "ruby", "java", "php",
		"deno", "bun", "elixir", "erlang", "swift", "zig", "crystal", "dotnet",
		"kotlin", "scala", "clojure", "haskell", "lua", "perl", "r", "julia",
		"nim", "ocaml", "dart", "flutter", "gleam":
		return "languages"
	case "postgres", "postgresql", "mysql", "mariadb", "redis", "mongodb", "sqlite",
		"cassandra", "elasticsearch", "clickhouse", "duckdb":
		return "databases"
	case "awscli", "aws-cli", "gcloud", "az", "azure-cli", "pulumi", "terraform",
		"terragrunt", "opentofu", "doctl", "flyctl", "heroku", "vercel",
		"cloudflared", "oci-cli":
		return "cloud"
	}
	// Heuristic fallbacks.
	if strings.Contains(t, "aws") || strings.Contains(t, "gcloud") || strings.Contains(t, "azure") {
		return "cloud"
	}
	return "tools"
}

// RuntimeIcon returns a lucide icon name suitable for the given tool.
func RuntimeIcon(tool, category string) string {
	switch strings.ToLower(tool) {
	case "node", "nodejs":
		return "hexagon"
	case "go", "golang":
		return "arrow-right"
	case "python":
		return "code"
	case "rust":
		return "cog"
	case "ruby", "crystal":
		return "gem"
	case "java":
		return "coffee"
	case "kubectl", "helm":
		return "ship"
	case "terraform", "pulumi":
		return "blocks"
	case "gh", "github":
		return "github"
	case "docker":
		return "container"
	}
	switch category {
	case "languages":
		return "code"
	case "databases":
		return "database"
	case "cloud":
		return "cloud"
	default:
		return "wrench"
	}
}
