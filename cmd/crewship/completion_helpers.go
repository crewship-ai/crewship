package main

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// completeAgentSlug provides shell-completion suggestions for any cobra
// command whose first positional arg is an agent slug. Wired in via
//
//	cmd.ValidArgsFunction = completeAgentSlug
//
// Behaviour: fetches /api/v1/agents and returns slugs whose prefix matches
// `toComplete`. Silent on any failure — there's no user-visible way to
// surface an error during tab-completion, and a broken completion function
// is worse than no completion (cobra will fall back to filename matching).
//
// Why this is its own file: keeps `cmd_run.go` etc free of completion
// plumbing so the runtime path stays easy to read.
func completeAgentSlug(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Only complete the first positional arg.
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	cfg, err := cli.LoadConfig()
	if err != nil || cfg.Token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	server := cli.ResolveServer(flagServer, cfg)
	workspace := cli.ResolveWorkspace(flagWorkspace, cfg)

	c := cli.NewClient(server, cfg.Token, workspace)
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer resp.Body.Close()

	var agents []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	out := make([]string, 0, len(agents))
	prefix := strings.ToLower(toComplete)
	for _, a := range agents {
		if a.Slug == "" {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(a.Slug), prefix) {
			suggestion := a.Slug
			if a.Name != "" {
				// Cobra uses tab to separate value from description;
				// rendered by zsh/fish as "slug -- description".
				suggestion = a.Slug + "\t" + a.Name
			}
			out = append(out, suggestion)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
