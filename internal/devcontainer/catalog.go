package devcontainer

import "strings"

// CatalogEntry describes a well-known devcontainer feature available in the
// UI picker. The catalog is statically embedded so the UI can render without
// hitting a registry.
type CatalogEntry struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`  // "languages", "tools", "cloud", "databases"
	Icon        string `json:"icon"`      // lucide icon name
	SizeHint    string `json:"size_hint"` // approximate installed size
}

// FallbackCatalog is the built-in list of popular devcontainer features. All entries
// reference the official devcontainers feature collection on ghcr.io.
//
// This catalog is used ONLY as a fallback when the dynamic upstream fetch
// (see catalog_fetcher.go) fails or is unavailable. For live data, use
// CatalogFetcher.GetCatalog.
var FallbackCatalog = []CatalogEntry{
	{
		Ref:         "ghcr.io/devcontainers/features/common-utils:2",
		Name:        "Common Utilities",
		Description: "Installs a set of common command line utilities, Oh My Zsh!, and sets up a non-root user.",
		Category:    "tools",
		Icon:        "wrench",
		SizeHint:    "~50 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/python:1",
		Name:        "Python",
		Description: "Installs the specified Python version, pip, and pipx. Supports virtual environments.",
		Category:    "languages",
		Icon:        "code",
		SizeHint:    "~80 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/node:1",
		Name:        "Node.js",
		Description: "Installs Node.js, nvm, yarn, and pnpm. Supports LTS and specific version selection.",
		Category:    "languages",
		Icon:        "hexagon",
		SizeHint:    "~100 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/go:1",
		Name:        "Go",
		Description: "Installs the Go compiler and tools. Includes golangci-lint and common Go utilities.",
		Category:    "languages",
		Icon:        "arrow-right",
		SizeHint:    "~200 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/rust:1",
		Name:        "Rust",
		Description: "Installs Rust, rustup, cargo, and common Rust development tools.",
		Category:    "languages",
		Icon:        "cog",
		SizeHint:    "~800 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/dotnet:2",
		Name:        ".NET",
		Description: "Installs the .NET SDK. Supports multiple versions and global tool installation.",
		Category:    "languages",
		Icon:        "hash",
		SizeHint:    "~600 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/github-cli:1",
		Name:        "GitHub CLI",
		Description: "Installs the GitHub CLI (gh) for interacting with GitHub from the command line.",
		Category:    "tools",
		Icon:        "github",
		SizeHint:    "~30 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/aws-cli:1",
		Name:        "AWS CLI",
		Description: "Installs the AWS CLI v2 for managing AWS services from the command line.",
		Category:    "cloud",
		Icon:        "cloud",
		SizeHint:    "~150 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/azure-cli:1",
		Name:        "Azure CLI",
		Description: "Installs the Azure CLI for managing Azure resources from the command line.",
		Category:    "cloud",
		Icon:        "cloud",
		SizeHint:    "~400 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/kubectl-helm-minikube:1",
		Name:        "Kubectl, Helm, and Minikube",
		Description: "Installs kubectl, Helm, and optionally Minikube for Kubernetes development.",
		Category:    "tools",
		Icon:        "ship",
		SizeHint:    "~100 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/docker-in-docker:2",
		Name:        "Docker-in-Docker",
		Description: "Installs Docker inside the dev container, enabling container builds and orchestration.",
		Category:    "tools",
		Icon:        "container",
		SizeHint:    "~200 MB",
	},
	{
		Ref:         "ghcr.io/devcontainers/features/terraform:1",
		Name:        "Terraform",
		Description: "Installs HashiCorp Terraform and optionally TFLint for infrastructure-as-code workflows.",
		Category:    "cloud",
		Icon:        "blocks",
		SizeHint:    "~80 MB",
	},
}

// FilterCatalog returns entries whose Name, Description, or Category contain
// the query string (case-insensitive). An empty query returns a copy of the
// full list.
func FilterCatalog(entries []CatalogEntry, query string) []CatalogEntry {
	if query == "" {
		result := make([]CatalogEntry, len(entries))
		copy(result, entries)
		return result
	}

	q := strings.ToLower(strings.TrimSpace(query))
	var results []CatalogEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(e.Ref), q) ||
			strings.Contains(strings.ToLower(e.Category), q) {
			results = append(results, e)
		}
	}
	return results
}
