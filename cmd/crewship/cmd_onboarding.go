package main

// CLI surface for the onboarding proposal store
// (docs/prd/conversational-onboarding.md §5.6, §8.2): the setup agent
// proposes a crew, and only a human's `apply` writes it. Every API endpoint
// gets a CLI command (CLAUDE.md) — this is the counterpart to
// internal/api/onboarding_proposal.go's Create / Get / Apply.

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var onboardingCmd = &cobra.Command{
	Use:   "onboarding",
	Short: "Onboarding proposal store — propose a crew, apply it under your own session",
}

var onboardingProposalCmd = &cobra.Command{
	Use:   "proposal",
	Short: "Create, inspect, and apply onboarding proposals",
}

// onboardingProposalAgentOut mirrors internal/api.onboardingProposalAgent —
// one agent in a proposal's resolved roster.
type onboardingProposalAgentOut struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	RoleTitle    string `json:"role_title"`
	LLMProvider  string `json:"llm_provider"`
	LLMModel     string `json:"llm_model"`
	SystemPrompt string `json:"system_prompt"`
}

// onboardingProposalPayloadOut mirrors internal/api.onboardingProposalPayload.
type onboardingProposalPayloadOut struct {
	CrewName     string                       `json:"crew_name"`
	CrewSlug     string                       `json:"crew_slug"`
	CrewIcon     *string                      `json:"crew_icon,omitempty"`
	TemplateSlug string                       `json:"template_slug"`
	LLMProvider  string                       `json:"llm_provider,omitempty"`
	LLMModel     string                       `json:"llm_model,omitempty"`
	Agents       []onboardingProposalAgentOut `json:"agents"`
}

// onboardingProposalOut mirrors internal/api.onboardingProposalResponse —
// the Create and Get response shape.
type onboardingProposalOut struct {
	ID            string                       `json:"id"`
	WorkspaceID   string                       `json:"workspace_id"`
	CreatedBy     string                       `json:"created_by"`
	CreatedAt     string                       `json:"created_at"`
	AppliedAt     *string                      `json:"applied_at"`
	Status        string                       `json:"status"`
	Payload       onboardingProposalPayloadOut `json:"payload"`
	AppliedCrewID *string                      `json:"applied_crew_id,omitempty"`
}

// onboardingProposalApplyOut mirrors internal/api.onboardingProposalApplyResponse.
type onboardingProposalApplyOut struct {
	ProposalID     string `json:"proposal_id"`
	Status         string `json:"status"`
	AlreadyApplied bool   `json:"already_applied"`
	Crew           struct {
		CrewID     string   `json:"crew_id"`
		CrewName   string   `json:"crew_name"`
		CrewSlug   string   `json:"crew_slug"`
		AgentCount int      `json:"agent_count"`
		AgentIDs   []string `json:"agent_ids"`
	} `json:"crew"`
}

// onboardingProposalDetailPairs renders the summary key/value list shared by
// `proposal create` and `proposal get`. Full agent-by-agent detail is always
// available via --output json|yaml (AutoDetail's fallthrough) — the table
// view here answers "what would clicking Apply do", not "reproduce the
// payload byte for byte".
func onboardingProposalDetailPairs(p onboardingProposalOut) [][]string {
	pairs := [][]string{
		{"ID", p.ID},
		{"Status", p.Status},
		{"Crew Name", p.Payload.CrewName},
		{"Crew Slug", p.Payload.CrewSlug},
		{"Template", p.Payload.TemplateSlug},
		{"Agents", fmt.Sprintf("%d", len(p.Payload.Agents))},
		{"Created At", p.CreatedAt},
	}
	if p.Payload.LLMModel != "" {
		pairs = append(pairs, []string{"Model Override", p.Payload.LLMProvider + " / " + p.Payload.LLMModel})
	}
	if p.AppliedAt != nil {
		pairs = append(pairs, []string{"Applied At", *p.AppliedAt})
	}
	if p.AppliedCrewID != nil {
		pairs = append(pairs, []string{"Applied Crew ID", *p.AppliedCrewID})
	}
	return pairs
}

var onboardingProposalCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Propose a crew from a template — writes nothing until `apply`",
	Long: `Create an onboarding proposal: resolve --template-slug plus an optional
model override into a full crew + agent roster, and store it server-side.

Nothing is created yet. The proposal's id is what 'crewship onboarding
proposal apply' takes, and apply reads ONLY the stored payload — this
command's flags never reach apply directly, by design (docs/prd/
conversational-onboarding.md §5.6).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		crewName, _ := flags.GetString("crew-name")
		templateSlug, _ := flags.GetString("template-slug")
		crewSlug, _ := flags.GetString("crew-slug")
		llmProvider, _ := flags.GetString("llm-provider")
		llmModel, _ := flags.GetString("llm-model")

		if crewName == "" {
			return fmt.Errorf("--crew-name is required")
		}
		if templateSlug == "" {
			return fmt.Errorf("--template-slug is required")
		}

		body := map[string]interface{}{
			"crew_name":     crewName,
			"template_slug": templateSlug,
		}
		if crewSlug != "" {
			body["crew_slug"] = crewSlug
		}
		if llmProvider != "" {
			body["llm_provider"] = llmProvider
		}
		if llmModel != "" {
			body["llm_model"] = llmModel
		}

		resp, err := client.Post("/api/v1/onboarding/proposals", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var proposal onboardingProposalOut
		if err := cli.ReadJSON(resp, &proposal); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Proposal created: %s (%d agent(s), template %s)",
			proposal.ID, len(proposal.Payload.Agents), proposal.Payload.TemplateSlug))
		f := newFormatter()
		return f.AutoDetail(proposal, onboardingProposalDetailPairs(proposal))
	},
}

var onboardingProposalGetCmd = &cobra.Command{
	Use:   "get <proposal-id>",
	Short: "Show a stored onboarding proposal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/onboarding/proposals/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var proposal onboardingProposalOut
		if err := cli.ReadJSON(resp, &proposal); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoDetail(proposal, onboardingProposalDetailPairs(proposal))
	},
}

var onboardingProposalApplyCmd = &cobra.Command{
	Use:   "apply <proposal-id>",
	Short: "Apply a proposal — the only write in this surface",
	Long: `Apply a stored onboarding proposal under YOUR OWN session. Runs
deployCrewTemplate from the proposal's stored template slug and model
override only — nothing on this command line reaches the write path.

Idempotent: applying an already-applied proposal returns the original
result again rather than creating a second crew.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		// No body: apply takes nothing but the id in the path (see the
		// handler's own doc comment on why that is not an oversight).
		resp, err := client.Post("/api/v1/onboarding/proposals/"+url.PathEscape(args[0])+"/apply", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out onboardingProposalApplyOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		headline := fmt.Sprintf("Crew created: %s (%s, %d agent(s))",
			out.Crew.CrewName, out.Crew.CrewID, out.Crew.AgentCount)
		if out.AlreadyApplied {
			headline = fmt.Sprintf("Already applied — returning original crew: %s (%s, %d agent(s))",
				out.Crew.CrewName, out.Crew.CrewID, out.Crew.AgentCount)
		}
		cli.PrintSuccess(headline)

		f := newFormatter()
		return f.AutoDetail(out, [][]string{
			{"Proposal ID", out.ProposalID},
			{"Status", out.Status},
			{"Already Applied", yesNo(out.AlreadyApplied)},
			{"Crew ID", out.Crew.CrewID},
			{"Crew Name", out.Crew.CrewName},
			{"Crew Slug", out.Crew.CrewSlug},
			{"Agent Count", fmt.Sprintf("%d", out.Crew.AgentCount)},
		})
	},
}

var onboardingSetupAgentCmd = &cobra.Command{
	Use:   "setup-agent",
	Short: "The in-wizard chat agent that helps stand up your crew",
}

// onboardingSetupAgentStartOut mirrors internal/api.onboardingSetupAgentStartResponse.
type onboardingSetupAgentStartOut struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

var onboardingSetupAgentStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start (or resume) the onboarding setup agent's chat session",
	Long: `Start the setup agent: find-or-create its crew, agent and chat for your
workspace, and return the ids needed to talk to it.

Idempotent: a second call (a page refresh, a remounted chat pane) returns the
same agent_id and session_id rather than standing up a second crew
(internal/api/onboarding_setup_agent.go's StartSetupAgent).

Requires a model credential on the workspace first — the setup agent runs in
a container and cannot answer without one. Add one with 'crewship credential
create' (or finish onboarding's Launch step) before running this.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/onboarding/setup-agent/start", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			// The 428 the server sends when the workspace has no model
			// credential yet is not a generic API failure: it names an
			// exact, actionable fix (internal/api/onboarding_setup_agent.go's
			// StartSetupAgent). Re-surface it as its own error instead of
			// letting "API error (428): …" stand as the only message — a
			// user hitting this before adding a credential should read the
			// cause and the fix, not a status code.
			var apiErr *cli.APIError
			if errors.As(err, &apiErr) && apiErr.Extensions["reason"] == "credential_required" {
				return cli.WithExitCode(fmt.Errorf(
					"setup agent needs a model credential first: %s\nadd one with 'crewship credential create', then run this again",
					apiErr.Detail,
				), cli.ExitValidation)
			}
			return err
		}
		var out onboardingSetupAgentStartOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Setup agent ready: agent %s, session %s", out.AgentID, out.SessionID))
		f := newFormatter()
		return f.AutoDetail(out, [][]string{
			{"Agent ID", out.AgentID},
			{"Session ID", out.SessionID},
		})
	},
}

func init() {
	onboardingProposalCreateCmd.Flags().String("crew-name", "", "Crew display name (required)")
	onboardingProposalCreateCmd.Flags().String("template-slug", "", "crew_templates slug to derive the roster from (required)")
	onboardingProposalCreateCmd.Flags().String("crew-slug", "", "Crew slug override (defaults to a slugified crew name)")
	onboardingProposalCreateCmd.Flags().String("llm-provider", "", "Model override provider: ANTHROPIC|OPENAI|GOOGLE|CURSOR|FACTORY|OLLAMA (default ANTHROPIC)")
	onboardingProposalCreateCmd.Flags().String("llm-model", "", "Model override — applied only to agents matching --llm-provider (Phase 1: template + model swap)")

	onboardingProposalCmd.AddCommand(onboardingProposalCreateCmd)
	onboardingProposalCmd.AddCommand(onboardingProposalGetCmd)
	onboardingProposalCmd.AddCommand(onboardingProposalApplyCmd)
	onboardingCmd.AddCommand(onboardingProposalCmd)

	onboardingSetupAgentCmd.AddCommand(onboardingSetupAgentStartCmd)
	onboardingCmd.AddCommand(onboardingSetupAgentCmd)

	rootCmd.AddCommand(onboardingCmd)
}
