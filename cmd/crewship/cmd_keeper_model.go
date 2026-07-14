package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/spf13/cobra"
)

// keeperModelCmd groups the M2a governance-model commands (issue #1001): the
// per-workspace choice of which model backs the Keeper access gatekeeper and
// aux evaluators, over the same partial-update PUT the other keeper subcommands
// use. Empty provider = "use the server/env default" (the pre-M2a behavior).
//
// Like the rest of the governance surface it is OWNER/ADMIN only. A credential
// ref points the provider at a vault ENDPOINT_URL / API_KEY credential; if that
// credential is later revoked the Keeper degrades to the default local Ollama
// judge rather than failing — so governance never loses its evaluator.
var keeperModelCmd = &cobra.Command{
	Use:   "model",
	Short: "Choose the governance model backing the Keeper (provider + model + credential)",
	Long: `Select the model the Keeper watchdog uses for this workspace: the access
gatekeeper and the auxiliary evaluators. Runs fully local on Ollama with no API
key, or points at Anthropic / an OpenAI-compatible endpoint via a vault credential.

  - provider: ollama | anthropic | openai_compat  (empty = server/env default)
  - model:    the provider's model id (e.g. qwen2.5:3b-instruct, claude-haiku-4-5)
  - credential (optional): a vault ENDPOINT_URL or API_KEY credential id the
      provider sources its endpoint/key from

Choosing a model does not enable the watchdog — run 'crewship keeper enable' for
that. OWNER/ADMIN only.

Examples:
  crewship keeper model get
  crewship keeper model set --provider ollama --model qwen2.5:3b-instruct
  crewship keeper model set --provider openai_compat --model gpt-4o-mini --credential cred_abc123
  crewship keeper model clear`,
}

// printKeeperModel renders the governance-model block shared by get + every
// mutation so the shape can't drift.
func printKeeperModel(gov keeperGovernance) {
	fmt.Printf("%sGovernance model (workspace)%s\n", cli.Bold, cli.Reset)
	if strings.TrimSpace(gov.GovModelProvider) == "" {
		fmt.Printf("  Provider:   — (server/env default)\n")
		return
	}
	fmt.Printf("  Provider:   %s\n", gov.GovModelProvider)
	fmt.Printf("  Model:      %s\n", gov.GovModelID)
	cred := gov.GovModelCredentialID
	if cred == "" {
		cred = "— (none — env/default endpoint)"
	}
	fmt.Printf("  Credential: %s\n", cred)
}

var keeperModelGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the current workspace governance model",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		gov, err := getKeeperGovernance(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(gov, func() { printKeeperModel(gov) })
	},
}

var (
	keeperModelSetProvider   string
	keeperModelSetModel      string
	keeperModelSetCredential string
)

var keeperModelSetCmd = &cobra.Command{
	Use:   "set --provider <p> --model <id> [--credential <id>]",
	Short: "Set the governance model provider + model (+ optional vault credential)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Mirror the server-side validation so the operator gets a specific
		// error before the round-trip (the server also enforces these).
		if !governance.KnownGovProvider(keeperModelSetProvider) {
			return fmt.Errorf("--provider must be one of: ollama, anthropic, openai_compat")
		}
		if strings.TrimSpace(keeperModelSetModel) == "" {
			return fmt.Errorf("--model is required")
		}
		if len(keeperModelSetModel) > governance.MaxGovModelIDLen {
			return fmt.Errorf("--model is %d bytes; the maximum is %d", len(keeperModelSetModel), governance.MaxGovModelIDLen)
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{
			"gov_model_provider":      keeperModelSetProvider,
			"gov_model_id":            keeperModelSetModel,
			"gov_model_credential_id": keeperModelSetCredential,
		})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Governance model updated for this workspace.")
			printKeeperModel(out)
		})
	},
}

var keeperModelClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the governance model (fall back to the server/env default)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{
			"gov_model_provider":      "",
			"gov_model_id":            "",
			"gov_model_credential_id": "",
		})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Governance model cleared — using the server/env default.")
			printKeeperModel(out)
		})
	},
}

func init() {
	keeperModelSetCmd.Flags().StringVar(&keeperModelSetProvider, "provider", "", "provider: ollama | anthropic | openai_compat")
	keeperModelSetCmd.Flags().StringVar(&keeperModelSetModel, "model", "", "model id for the provider")
	keeperModelSetCmd.Flags().StringVar(&keeperModelSetCredential, "credential", "", "optional vault ENDPOINT_URL/API_KEY credential id")

	keeperModelCmd.AddCommand(keeperModelGetCmd)
	keeperModelCmd.AddCommand(keeperModelSetCmd)
	keeperModelCmd.AddCommand(keeperModelClearCmd)

	keeperCmd.AddCommand(keeperModelCmd)
}
