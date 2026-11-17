package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func newCredentialBedrockAssignCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bedrock-assign <credential-name-or-id>", Short: "Assign a Bedrock account and native model to agents or crews", Args: cobra.ExactArgs(1)}
	cmd.Flags().String("model", "", "Permitted native Bedrock model ID (without amazon-bedrock/)")
	cmd.Flags().StringArray("agent", nil, "Agent slug or ID; repeat to assign several")
	cmd.Flags().StringArray("crew", nil, "Crew slug or ID; expands to its current agents")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		model := strings.TrimSpace(mustFlagString(cmd, "model"))
		agents, _ := cmd.Flags().GetStringArray("agent")
		crews, _ := cmd.Flags().GetStringArray("crew")
		if model == "" || len(agents)+len(crews) == 0 {
			return fmt.Errorf("--model and at least one --agent or --crew are required")
		}
		client := newAPIClient()
		id, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		for i, a := range agents {
			agents[i], err = resolveAgentID(client, a)
			if err != nil {
				return err
			}
		}
		for i, c := range crews {
			crews[i], err = resolveCrewID(client, c)
			if err != nil {
				return err
			}
		}
		resp, err := client.Post("/api/v1/credentials/"+url.PathEscape(id)+"/bedrock-assign", map[string]any{"model": strings.TrimPrefix(model, "amazon-bedrock/"), "agent_ids": agents, "crew_ids": crews})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			Assigned int    `json:"assigned"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		return newFormatter().AutoHuman(result, func() { fmt.Printf("Assigned %s through OpenCode to %d agents.\n", result.Model, result.Assigned) })
	}
	return cmd
}

func init() { credentialCmd.AddCommand(newCredentialBedrockAssignCmd()) }
