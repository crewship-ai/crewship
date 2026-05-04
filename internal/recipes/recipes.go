// Package recipes implements the 1-click "connect recipe" feature
// (CONNECTIONS.md §6, project_credentials_integrations_strategy.md
// recipes empty state). A recipe is a hardcoded bundle of:
//
//   - a curated crew identity (name, slug, icon, color)
//   - the credentials it needs (provider + env_var_name + UX label)
//   - the MCP servers it should run (transport, command, env mapping)
//
// User flow:
//
//  1. GET  /api/v1/recipes                — browse curated bundles
//  2. POST /api/v1/recipes/:slug/preview  — dry run, FE shows what
//     will be created and
//     which credentials it
//     still needs to collect
//  3. POST /api/v1/recipes/:slug/install  — atomic create: all
//     credentials + MCP
//     servers + crew commit
//     in one transaction; any
//     failure rolls back
//     everything.
//
// Recipes are baked into the binary for MVP. A future ticket may
// promote them to a DB table so admins can author their own — but
// CONNECTIONS.md §6 explicitly chose hardcoded for the launch scope.
package recipes

// Recipe is the static blueprint for a 1-click bundle.
type Recipe struct {
	// Slug is the URL-stable identifier ("code-review-crew").
	Slug string `json:"slug"`

	// Name is the human label shown on the dashboard cards.
	Name string `json:"name"`

	// Description is the one-line tagline shown under the name.
	Description string `json:"description"`

	// Icon + Color match the CrewIcon component palette.
	Icon  string `json:"icon"`
	Color string `json:"color"`

	// CrewSlug is the slug the new crew will get. Independent of
	// the recipe slug so two installs of the same recipe are
	// possible (the second one gets a "-2" suffix at install time).
	CrewSlug string `json:"crew_slug"`

	// Credentials are the secrets the recipe needs. Each entry tells
	// the FE what to prompt for; the actual values arrive in the
	// install request body.
	Credentials []RecipeCredential `json:"credentials"`

	// MCPServers describes the integrations to provision on the new
	// crew. Each binds back to a credential by EnvVarRef so the
	// install flow can wire credentials into the env_json.
	MCPServers []RecipeMCPServer `json:"mcp_servers"`
}

// RecipeCredential describes one credential a recipe needs.
type RecipeCredential struct {
	// EnvVarName is what the agent will see at runtime
	// (ANTHROPIC_API_KEY, GH_TOKEN, ...). Doubles as the credential
	// name inside the workspace per existing convention.
	EnvVarName string `json:"env_var_name"`

	// Provider is the canonical provider enum (ANTHROPIC, GITHUB,
	// OPENAI, ...). Picked from the same set as the credentials
	// table column.
	Provider string `json:"provider"`

	// Type is the credential type (API_KEY, AI_CLI_TOKEN, OAUTH2,
	// CLI_TOKEN, SECRET).
	Type string `json:"type"`

	// Label is the human-readable prompt shown in the install
	// wizard ("Anthropic API key").
	Label string `json:"label"`

	// HelpURL points the user at the provider's "where do I find
	// this?" doc when populated.
	HelpURL string `json:"help_url,omitempty"`
}

// RecipeMCPServer describes one MCP server to provision on the
// recipe's crew.
type RecipeMCPServer struct {
	// Name is the URL-stable name on the crew (matches
	// crew_mcp_servers.name UNIQUE constraint).
	Name string `json:"name"`

	// DisplayName is what shows in the connected list and
	// marketplace cards.
	DisplayName string `json:"display_name"`

	// Transport is "stdio" or "streamable-http".
	Transport string `json:"transport"`

	// Command + Args describe how to launch a stdio server.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// Endpoint is the URL for HTTP transports.
	Endpoint string `json:"endpoint,omitempty"`

	// EnvMapping declares which env vars the server expects, keyed
	// by the env var name and valued by the EnvVarName from the
	// recipe's Credentials list. The install flow turns this into
	// the env_json that the integrations handler stores.
	EnvMapping map[string]string `json:"env_mapping,omitempty"`

	// Icon hints at the brand logo to render alongside the entry.
	// Mirrors mcp_registry_servers.icon semantics.
	Icon string `json:"icon,omitempty"`
}

// builtins is the curated MVP recipe set. Order is the display order
// on the dashboard empty state.
var builtins = []Recipe{
	{
		Slug:        "code-review-crew",
		Name:        "Code review crew",
		Description: "Anthropic-powered agent that reviews your GitHub pull requests.",
		Icon:        "git-pull-request",
		Color:       "blue",
		CrewSlug:    "code-review",
		Credentials: []RecipeCredential{
			{
				EnvVarName: "ANTHROPIC_API_KEY",
				Provider:   "ANTHROPIC",
				Type:       "API_KEY",
				Label:      "Anthropic API key",
				HelpURL:    "https://console.anthropic.com/settings/keys",
			},
			{
				EnvVarName: "GH_TOKEN",
				Provider:   "GITHUB",
				Type:       "CLI_TOKEN",
				Label:      "GitHub personal access token",
				HelpURL:    "https://github.com/settings/tokens",
			},
		},
		MCPServers: []RecipeMCPServer{
			{
				Name:        "github",
				DisplayName: "GitHub",
				Transport:   "stdio",
				Command:     "npx",
				Args:        []string{"-y", "@modelcontextprotocol/server-github"},
				Icon:        "github",
				EnvMapping:  map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "GH_TOKEN"},
			},
		},
	},
	{
		Slug:        "triage-crew",
		Name:        "Triage crew",
		Description: "OpenAI-powered agent that triages your Linear backlog.",
		Icon:        "list-checks",
		Color:       "violet",
		CrewSlug:    "triage",
		Credentials: []RecipeCredential{
			{
				EnvVarName: "OPENAI_API_KEY",
				Provider:   "OPENAI",
				Type:       "API_KEY",
				Label:      "OpenAI API key",
				HelpURL:    "https://platform.openai.com/api-keys",
			},
			{
				EnvVarName: "LINEAR_API_KEY",
				Provider:   "NONE",
				Type:       "API_KEY",
				Label:      "Linear API key",
				HelpURL:    "https://linear.app/settings/api",
			},
		},
		MCPServers: []RecipeMCPServer{
			{
				Name:        "linear",
				DisplayName: "Linear",
				Transport:   "stdio",
				Command:     "npx",
				Args:        []string{"-y", "@linear/mcp-server"},
				Icon:        "linear",
				EnvMapping:  map[string]string{"LINEAR_API_KEY": "LINEAR_API_KEY"},
			},
		},
	},
	{
		Slug:        "research-crew",
		Name:        "Research crew",
		Description: "Anthropic-powered agent with Brave Search for web research.",
		Icon:        "telescope",
		Color:       "amber",
		CrewSlug:    "research",
		Credentials: []RecipeCredential{
			{
				EnvVarName: "ANTHROPIC_API_KEY",
				Provider:   "ANTHROPIC",
				Type:       "API_KEY",
				Label:      "Anthropic API key",
				HelpURL:    "https://console.anthropic.com/settings/keys",
			},
			// Brave Search MCP needs an API key (free tier exists at
			// brave.com/search/api). Required here so install collects
			// it; otherwise the server provisions but produces 401s on
			// every call. CodeRabbit caught the original "comment-only"
			// declaration that left the server unauthenticated.
			{
				EnvVarName: "BRAVE_API_KEY",
				Provider:   "NONE",
				Type:       "API_KEY",
				Label:      "Brave Search API key",
				HelpURL:    "https://brave.com/search/api/",
			},
		},
		MCPServers: []RecipeMCPServer{
			{
				Name:        "brave-search",
				DisplayName: "Brave Search",
				Transport:   "stdio",
				Command:     "npx",
				Args:        []string{"-y", "@modelcontextprotocol/server-brave-search"},
				Icon:        "search",
				EnvMapping:  map[string]string{"BRAVE_API_KEY": "BRAVE_API_KEY"},
			},
		},
	},
}

// All returns the static recipe list. Returns by value (slice header
// copy) — callers must not mutate the embedded slices/maps.
func All() []Recipe {
	out := make([]Recipe, len(builtins))
	copy(out, builtins)
	return out
}

// FindBySlug returns a recipe by slug, or nil if not present.
func FindBySlug(slug string) *Recipe {
	for i := range builtins {
		if builtins[i].Slug == slug {
			return &builtins[i]
		}
	}
	return nil
}
