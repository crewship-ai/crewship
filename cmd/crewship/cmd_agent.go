package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/cli"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all agents in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		limit, _ := cmd.Flags().GetInt("limit")
		offset, _ := cmd.Flags().GetInt("offset")
		q := url.Values{}
		setListPaging(q, limit, offset)
		if crewFilter, _ := cmd.Flags().GetString("crew"); crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
		}
		path := "/api/v1/agents"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var agents []agentListItem
		if err := cli.ReadJSON(resp, &agents); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "ROLE", "CREW", "STATUS", "ADAPTER", "MEMORY"}
		var rows [][]string
		for _, a := range agents {
			crewName := "-"
			if a.Crew != nil {
				crewName = a.Crew.Slug
			}
			mem := "off"
			if a.MemoryEnabled {
				mem = "on"
			}
			rows = append(rows, []string{a.Slug, a.AgentRole, crewName, a.Status, a.CLIAdapter, mem})
		}
		defer printListFooter(f, readListMeta(resp), len(agents))
		return f.Auto(agents, headers, rows)
	},
}

var agentGetCmd = &cobra.Command{
	Use:               "get <slug-or-id>",
	Short:             "Show agent details",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		// #1177: one request on the CUID fast path (verify == fetch).
		resp, _, err := getByRef(client, "/api/v1/agents/", args[0], resolveAgentID)
		if err != nil {
			return err
		}

		var agent agentDetailResponse
		if err := cli.ReadJSON(resp, &agent); err != nil {
			return err
		}

		f := newFormatter()
		crewName := "-"
		if agent.Crew != nil {
			crewName = agent.Crew.Slug
		}
		mem := "off"
		if agent.MemoryEnabled {
			mem = "on"
		}
		pairs := [][]string{
			{"Name", agent.Name},
			{"Slug", agent.Slug},
			{"ID", agent.ID},
			{"Role", agent.AgentRole},
			{"Crew", crewName},
			{"Status", agent.Status},
			{"CLI Adapter", agent.CLIAdapter},
			{"Tool Profile", agent.ToolProfile},
			{"Memory", mem},
			{"Timeout", fmt.Sprintf("%ds", agent.TimeoutSeconds)},
			{"Created", agent.CreatedAt},
			{"Skills", fmt.Sprintf("%d", agent.Count.Skills)},
			{"Credentials", fmt.Sprintf("%d", agent.Count.Credentials)},
		}
		if agent.RoleTitle != nil {
			pairs = append([][]string{pairs[0], pairs[1], pairs[2], {"Role Title", *agent.RoleTitle}}, pairs[3:]...)
		}
		// Schedule rows only when a cron is actually configured — an
		// unscheduled agent's detail stays lean. When set, show whether it's
		// firing (enabled/disabled), the prompt, and the resolved last/next
		// run so the operator can confirm the cron is live end-to-end.
		// Webhook secret presence (never the value — show-once, #999).
		// Hidden entirely against servers predating the field.
		if agent.WebhookSecretSet != nil {
			wh := "not configured (rotate to mint one)"
			if *agent.WebhookSecretSet {
				wh = "configured (rotate-webhook-secret to replace)"
			}
			pairs = append(pairs, []string{"Webhook secret", wh})
		}
		if agent.ScheduleCron != nil && *agent.ScheduleCron != "" {
			state := "disabled"
			if agent.ScheduleEnabled {
				state = "enabled"
			}
			pairs = append(pairs, []string{"Schedule", *agent.ScheduleCron + " (" + state + ")"})
			if agent.SchedulePrompt != nil && *agent.SchedulePrompt != "" {
				pairs = append(pairs, []string{"Schedule Prompt", *agent.SchedulePrompt})
			}
			if agent.ScheduleNextRun != nil && *agent.ScheduleNextRun != "" {
				pairs = append(pairs, []string{"Next Run", *agent.ScheduleNextRun})
			}
			if agent.ScheduleLastRun != nil && *agent.ScheduleLastRun != "" {
				pairs = append(pairs, []string{"Last Run", *agent.ScheduleLastRun})
			}
		}
		// Chat suggestions, listed one per line under a single label so
		// `agent get` shows what `agent update --suggested-prompts` wrote.
		// Absent when unconfigured — that is the majority case, and it means
		// the role defaults are in use, not that anything is missing.
		if agent.SuggestedPrompts != nil && *agent.SuggestedPrompts != "" {
			for i, p := range strings.Split(*agent.SuggestedPrompts, "\n") {
				label := ""
				if i == 0 {
					label = "Suggested Prompts"
				}
				pairs = append(pairs, []string{label, p})
			}
		}
		// Ask forms, one row per form: the label a person clicks, its id (the
		// handle `agent ask-preview` takes) and how many fields it asks for.
		// The definitions themselves are a JSON document and belong in a file,
		// not in a detail view — what this answers is "which forms does this
		// agent have, and what do I type to render one".
		if agent.AskForms != nil && strings.TrimSpace(*agent.AskForms) != "" {
			if forms, err := askforms.Parse(*agent.AskForms); err == nil {
				for i, form := range forms {
					label := ""
					if i == 0 {
						label = "Ask Forms"
					}
					pairs = append(pairs, []string{label,
						fmt.Sprintf("%s (%s, %d fields)", form.Label, form.ID, len(form.Fields))})
				}
			}
		}
		// Surface the webhook replay policy only when it's on (#815) — the
		// default-off case stays off the lean detail view.
		if agent.WebhookRequireTimestamp {
			pairs = append(pairs, []string{"Webhook Auth", "timestamped signature required"})
		}
		return f.AutoDetail(agent, pairs)
	},
}

func init() {
	agentListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	addListPagingFlags(agentListCmd.Flags(), 0)

	agentCreateCmd.Flags().String("name", "", "Agent name (required)")
	agentCreateCmd.Flags().String("slug", "", "Agent slug (auto-generated from name if empty)")
	agentCreateCmd.Flags().String("crew", "", "Crew slug or ID")
	agentCreateCmd.Flags().String("role", "AGENT", "Agent role: AGENT or LEAD")
	// COORDINATOR is refused here rather than forwarded — see
	// refuseRetiredRole. Same hook on update.
	agentCreateCmd.PreRunE = refuseRetiredRole
	agentUpdateCmd.PreRunE = refuseRetiredRole
	agentCreateCmd.Flags().String("role-title", "", "Human-readable role title")
	agentCreateCmd.Flags().String("cli-adapter", "CLAUDE_CODE", "CLI adapter: CLAUDE_CODE|CODEX_CLI|GEMINI_CLI|OPENCODE|CURSOR_CLI|FACTORY_DROID")
	agentCreateCmd.Flags().String("system-prompt", "", "System prompt text or @file.txt")
	agentCreateCmd.Flags().String("tool-profile", "CODING", "Tool profile: MINIMAL|CODING|FULL")
	agentCreateCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC|OPENAI|GOOGLE")
	agentCreateCmd.Flags().String("llm-model", "", "LLM model (e.g., claude-haiku-4-5)")
	agentCreateCmd.Flags().Bool("memory", false, "Enable memory")
	agentCreateCmd.Flags().String("lead-mode", "", "Lead mode: active|passive")
	agentCreateCmd.Flags().Int("timeout", 0, "Timeout in seconds")
	agentCreateCmd.Flags().String("avatar-seed", "", "Avatar seed (defaults to agent name)")
	agentCreateCmd.Flags().String("avatar-style", "", "Avatar style: bottts-neutral|adventurer|fun-emoji|pixel-art|micah|notionists|thumbs|lorelei|big-smile|avataaars")

	agentUpdateCmd.Flags().String("name", "", "Agent name")
	agentUpdateCmd.Flags().String("role", "", "Agent role")
	agentUpdateCmd.Flags().String("role-title", "", "Human-readable role title")
	agentUpdateCmd.Flags().String("cli-adapter", "", "CLI adapter")
	agentUpdateCmd.Flags().String("system-prompt", "", "System prompt text or @file.txt")
	agentUpdateCmd.Flags().String("tool-profile", "", "Tool profile")
	agentUpdateCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC|OPENAI|GOOGLE")
	agentUpdateCmd.Flags().String("llm-model", "", "LLM model")
	agentUpdateCmd.Flags().Bool("memory", false, "Enable memory")
	agentUpdateCmd.Flags().String("suggested-prompts", "",
		"Chat suggestions shown as buttons under an empty chat: one per line, max 8, max 120 chars each. Text or @file.txt; pass \"\" to clear")
	agentUpdateCmd.Flags().String("ask-forms", "",
		"Ask forms as a JSON array: max 4 forms, max 6 fields each. Usually @forms.json; pass \"\" to clear. Preview one with `agent ask-preview`")
	agentUpdateCmd.Flags().Bool("self-learning", false, "Enable/disable per-agent self-learning (requires --learning-reason for audit)")
	agentUpdateCmd.Flags().String("learning-reason", "", "Audit reason recorded with the --self-learning change (required when --self-learning is set)")
	agentUpdateCmd.Flags().String("lead-mode", "", "Lead mode")
	agentUpdateCmd.Flags().Int("timeout", 0, "Timeout in seconds")
	agentUpdateCmd.Flags().String("schedule-cron", "", "Cron expression to run the agent on a schedule (e.g. '*/5 * * * *'); empty clears it")
	agentUpdateCmd.Flags().String("schedule-prompt", "", "Prompt the scheduled run sends to the agent")
	agentUpdateCmd.Flags().Bool("schedule-enabled", false, "Enable/disable the agent's cron schedule")
	agentUpdateCmd.Flags().Bool("webhook-require-timestamp", false, "Require inbound webhooks to use the timestamped signature scheme (rejects replayable body-only/plaintext deliveries)")
	agentUpdateCmd.Flags().String("avatar-seed", "", "Avatar seed")
	agentUpdateCmd.Flags().String("avatar-style", "", "Avatar style")

	agentDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	agentLogsCmd.Flags().Int("tail", 100, "Number of log lines to show")

	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentGetCmd)
	agentCmd.AddCommand(agentCreateCmd)
	agentCmd.AddCommand(agentUpdateCmd)
	agentCmd.AddCommand(agentRotateWebhookSecretCmd)
	agentCmd.AddCommand(agentDeleteCmd)
	agentCmd.AddCommand(agentRunsCmd)
	agentCmd.AddCommand(agentStopCmd)
	agentCmd.AddCommand(agentLogsCmd)
	agentCmd.AddCommand(agentDebugCmd)
	agentCmd.AddCommand(agentSkillsCmd)
	agentCmd.AddCommand(agentCredentialsCmd)
}

// Resolver helpers and shared types

type agentListItem struct {
	ID            string          `json:"id"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	AgentRole     string          `json:"agent_role"`
	Status        string          `json:"status"`
	CLIAdapter    string          `json:"cli_adapter"`
	MemoryEnabled bool            `json:"memory_enabled"`
	Crew          *agentCrewShort `json:"crew"`
}

type agentCrewShort struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type agentDetailResponse struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	AgentRole      string          `json:"agent_role"`
	RoleTitle      *string         `json:"role_title"`
	Status         string          `json:"status"`
	CLIAdapter     string          `json:"cli_adapter"`
	ToolProfile    string          `json:"tool_profile"`
	MemoryEnabled  bool            `json:"memory_enabled"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	CreatedAt      string          `json:"created_at"`
	Crew           *agentCrewShort `json:"crew"`
	// Schedule (cron) fields — the read side of the `agent update
	// --schedule-*` flags. The API always returns these; surfacing them
	// here lets an operator confirm a cron landed (and see last/next run)
	// via the CLI instead of the raw API.
	ScheduleCron    *string `json:"schedule_cron"`
	SchedulePrompt  *string `json:"schedule_prompt"`
	ScheduleEnabled bool    `json:"schedule_enabled"`
	ScheduleLastRun *string `json:"schedule_last_run"`
	ScheduleNextRun *string `json:"schedule_next_run"`
	// WebhookRequireTimestamp is the read side of `agent update
	// --webhook-require-timestamp` (#815).
	WebhookRequireTimestamp bool `json:"webhook_require_timestamp"`
	// SuggestedPrompts is the read side of `agent update
	// --suggested-prompts`: the agent's own chat suggestions, one per line.
	// nil/empty means unconfigured, i.e. the chat shows the role defaults.
	SuggestedPrompts *string `json:"suggested_prompts"`
	// AskForms is the read side of `agent update --ask-forms`: the agent's
	// questionnaires, as the canonical JSON document the server stored. Also
	// what `agent ask-preview` renders from. Pointer: nil on servers
	// predating the column, so the CLI stays silent rather than claiming the
	// agent has none.
	AskForms *string `json:"ask_forms"`
	// WebhookSecretSet reports whether a webhook signing secret is
	// configured (#999). The value itself is show-once — obtain one via
	// `agent rotate-webhook-secret`. Pointer: nil on servers predating
	// the field, so the CLI can stay silent instead of claiming "none".
	WebhookSecretSet *bool `json:"webhook_secret_set"`
	Count            struct {
		Skills      int `json:"skills"`
		Credentials int `json:"credentials"`
	} `json:"_count"`
}

// refuseRetiredRole rejects --role COORDINATOR on agent create/update
// before the request is built.
//
// #2189: this used to warn "Setting role anyway" and forward the value,
// and its comment claimed the role was still accepted for back-compat
// with v1 templates. Neither was true — COORDINATOR was retired in v0.1
// and the handler answers 400 "agent_role must be AGENT or LEAD"
// (internal/api/agents.go, pinned by agents_test.go). So the one person
// that path existed for, applying an old template, was told the value
// would be honoured and then got a server refusal naming neither the
// template nor the deprecation they had just been warned about.
//
// Refusing locally costs a round trip and, more to the point, lets the
// message carry the fix. This is NOT a general role validator: every
// other value goes to the server, which owns what is valid.
func refuseRetiredRole(cmd *cobra.Command, _ []string) error {
	v, _ := cmd.Flags().GetString("role")
	if strings.EqualFold(v, "COORDINATOR") {
		// ExitValidation, not the default ExitGeneric: this refusal stands
		// in for the server's 400, and moving a check from the server to
		// the client must not change what the shell sees.
		return cli.WithExitCode(
			fmt.Errorf("COORDINATOR was retired in v0.1 and the server rejects it; use --role LEAD"),
			cli.ExitValidation)
	}
	return nil
}
