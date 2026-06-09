package main

import (
	"fmt"
	"sort"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// modelCmd groups model-discovery subcommands. CLI parity for
// GET /api/v1/models — agents use this to find out what they can set as
// llm_model before patching an agent.
var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "Discover the models a provider can serve",
}

// modelInfoRow mirrors llm.ModelInfo / the API's modelsListResponse.models.
type modelInfoRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Provider    string `json:"provider"`
}

type modelListResult struct {
	Provider string         `json:"provider"`
	Source   string         `json:"source"`
	Models   []modelInfoRow `json:"models"`
}

var modelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List models for a provider (live when a credential exists, else curated)",
	Long: `List the models a provider can serve.

When the workspace has an active API key for the provider, the list is fetched
live from the provider. Otherwise a curated fallback set is returned. The
"source" field reports which.

Examples:
  crewship model list --provider anthropic
  crewship model list --provider openai --format json
  crewship model list --provider ollama   # live-only; needs a reachable daemon`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		provider, _ := cmd.Flags().GetString("provider")
		if provider == "" {
			return fmt.Errorf("--provider is required (anthropic, openai, google, ollama)")
		}

		res, err := fetchModels(client, provider)
		if err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(res)
		}
		if f.Format == "yaml" {
			return f.YAML(res)
		}
		printModelList(res)
		return nil
	},
}

// fetchModels calls GET /api/v1/models?provider= and decodes the response.
func fetchModels(c *cli.Client, provider string) (*modelListResult, error) {
	resp, err := c.Get("/api/v1/models" + queryString("provider", provider))
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var res modelListResult
	if err := cli.ReadJSON(resp, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func printModelList(res *modelListResult) {
	fmt.Printf("%s%s models%s  (source=%s, %d total)\n",
		cli.Bold, res.Provider, cli.Reset, res.Source, len(res.Models))
	rows := make([]modelInfoRow, len(res.Models))
	copy(rows, res.Models)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, m := range rows {
		name := m.DisplayName
		if name == "" || name == m.ID {
			fmt.Printf("  %s\n", m.ID)
			continue
		}
		fmt.Printf("  %-32s %s%s%s\n", m.ID, cli.Dim, name, cli.Reset)
	}
}

func init() {
	modelListCmd.Flags().String("provider", "", "Provider to list models for (anthropic, openai, google, ollama)")
	modelCmd.AddCommand(modelListCmd)
}
