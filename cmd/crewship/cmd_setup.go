package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

// `crewship setup` is the CLI-side counterpart to the web onboarding
// wizard. After `crewship login --pair` mints a CLI token, the user
// has an authenticated session but no crew — the wizard hasn't run.
// This command closes that gap so a user who prefers the terminal
// can complete onboarding without opening a browser.
//
// Interactive by default: prompts for workspace name, language, crew
// template, adapter, and the per-adapter CLI token (output of
// `claude setup-token`, `gemini auth print-token`, etc. — NOT the
// vendor's account-level API key from their console). Each prompt
// has a sensible default so a user can hit Enter through it.
// All fields are also flag-overridable for scripting / CI.
//
// Hits the same POST /api/v1/onboarding/setup endpoint as the
// browser, so the server-side validation, language injection, and
// template deploy behave identically across both surfaces.

var (
	setupWorkspaceFlag string
	setupLanguageFlag  string
	setupCrewFlag      string
	setupAdapterFlag   string
	setupModelFlag     string
	setupAPIKeyFlag    string
	setupYesFlag       bool
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Complete onboarding (workspace + first crew + adapter) from the terminal",
	Long: `Finish setting up a freshly paired Crewship instance.

After ` + "`crewship login --pair`" + ` you'll be authenticated but have no
crew yet — that step normally happens in the browser onboarding
wizard. This command runs the same flow from your terminal.

Interactive mode (recommended):
  crewship setup

Non-interactive / scripted:
  crewship setup --crew=software-development --adapter=CLAUDE_CODE \
                 --token=$(claude setup-token) --yes

Available crew templates:
  software-development   Tech Lead + Backend + Frontend + QA (4 agents)
  devops-sre             SRE Lead + Platform + Security + CI/CD (4 agents)
  content-marketing      Lead + Researcher + Copy + SEO (4 agents)
  accounting-finance     Lead + Bookkeeper + Tax + Reporting (4 agents)
  blank                  Single agent of your choosing (1 agent)`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVar(&setupWorkspaceFlag, "workspace-name", "", "Display name for your workspace (optional, keeps existing if blank)")
	setupCmd.Flags().StringVar(&setupLanguageFlag, "language", "", "Language for agent replies (e.g. English, Čeština, Deutsch)")
	setupCmd.Flags().StringVar(&setupCrewFlag, "crew", "", "Crew template slug (software-development | devops-sre | content-marketing | accounting-finance | blank)")
	setupCmd.Flags().StringVar(&setupAdapterFlag, "adapter", "", "CLI adapter (CLAUDE_CODE | OPENCODE | CODEX_CLI | GEMINI_CLI | CURSOR_CLI | FACTORY_DROID)")
	setupCmd.Flags().StringVar(&setupModelFlag, "model", "", "LLM model (defaults to the adapter's recommended model)")
	setupCmd.Flags().StringVar(&setupAPIKeyFlag, "token", "", "CLI token (output of `claude setup-token`, `gemini auth print-token`, etc. — NOT the vendor's API key)")
	setupCmd.Flags().StringVar(&setupAPIKeyFlag, "api-key", "", "Deprecated alias for --token (will be removed)")
	_ = setupCmd.Flags().MarkDeprecated("api-key", "use --token instead — onboarding accepts CLI tokens, not raw API keys")
	setupCmd.Flags().BoolVar(&setupYesFlag, "yes", false, "Skip interactive prompts; require all values via flags")

	rootCmd.AddCommand(setupCmd)
}

// supportedCrewTemplates mirrors what seed_crew_templates.go inserts
// — kept in this file as a small const list (rather than fetched
// from /api/v1/crew-templates) so `crewship setup --help` is
// usable on an air-gapped machine without round-tripping to the
// server.
var supportedCrewTemplates = []struct {
	slug, label string
}{
	{"software-development", "Software Development (Tech Lead, Backend, Frontend, QA)"},
	{"devops-sre", "DevOps / SRE (SRE Lead, Platform, Security, CI/CD)"},
	{"content-marketing", "Content Marketing (Lead, Researcher, Copy, SEO)"},
	{"accounting-finance", "Accounting & Finance (Lead, Bookkeeper, Tax, Reporting)"},
	{"blank", "Blank (single agent, name yourself)"},
}

var supportedAdapters = []struct {
	key, label, envVar, provider, defaultModel string
}{
	{"CLAUDE_CODE", "Claude Code (Anthropic)", "ANTHROPIC_API_KEY", "ANTHROPIC", "claude-sonnet-4-6"},
	{"GEMINI_CLI", "Gemini CLI (Google)", "GOOGLE_API_KEY", "GOOGLE", "gemini-2.5-pro"},
	{"CODEX_CLI", "Codex CLI (OpenAI)", "OPENAI_API_KEY", "OPENAI", "gpt-5.5"},
	{"OPENCODE", "OpenCode", "ANTHROPIC_API_KEY", "ANTHROPIC", "anthropic/claude-sonnet-4-6"},
	{"CURSOR_CLI", "Cursor CLI", "CURSOR_API_KEY", "CURSOR", "composer"},
	{"FACTORY_DROID", "Factory Droid", "FACTORY_API_KEY", "FACTORY", "claude-sonnet-4-6"},
}

func runSetup(cmd *cobra.Command, _ []string) error {
	if err := requireAuth(); err != nil {
		return fmt.Errorf("not logged in — run `crewship login --pair --code=…` first")
	}

	workspaceName := setupWorkspaceFlag
	language := setupLanguageFlag
	crewSlug := setupCrewFlag
	adapter := setupAdapterFlag
	model := setupModelFlag
	apiKey := setupAPIKeyFlag

	interactive := !setupYesFlag && term.IsTerminal(int(os.Stdin.Fd()))

	if crewSlug == "" {
		if !interactive {
			return errors.New("--crew is required in non-interactive mode")
		}
		var err error
		crewSlug, err = promptCrew()
		if err != nil {
			return err
		}
	}
	if !isValidCrewSlug(crewSlug) {
		return fmt.Errorf("unknown crew template %q — see `crewship setup --help` for the list", crewSlug)
	}

	if adapter == "" {
		if !interactive {
			return errors.New("--adapter is required in non-interactive mode")
		}
		var err error
		adapter, err = promptAdapter()
		if err != nil {
			return err
		}
	}
	adapterCfg, ok := lookupAdapter(adapter)
	if !ok {
		return fmt.Errorf("unknown adapter %q — see `crewship setup --help`", adapter)
	}
	if model == "" {
		model = adapterCfg.defaultModel
	}

	if apiKey == "" {
		// Fall back to the env var the adapter would normally read
		// before prompting — most users already have it set.
		if interactive {
			var err error
			apiKey, err = promptAPIKey(adapterCfg.label)
			if err != nil {
				return err
			}
		} else {
			return fmt.Errorf("no token provided — pass --token=$(claude setup-token) (or the equivalent for %s)", adapterCfg.label)
		}
	}
	if len(apiKey) < 8 {
		return errors.New("token looks too short (need at least 8 characters)")
	}
	if adapterCfg.provider == "ANTHROPIC" && strings.HasPrefix(apiKey, "sk-ant-api") {
		return errors.New("that looks like an Anthropic API key (sk-ant-api…). Crewship needs the CLI token from `claude setup-token` (sk-ant-oat… value) — run that command on your machine and paste the result")
	}

	if language == "" && interactive {
		language = promptOptional("Agent language (e.g. English, Čeština) [English]", "English")
	}

	body := map[string]any{
		"workspace_name":     workspaceName,
		"preferred_language": language,
		"cli_adapter":        adapter,
		"llm_provider":       adapterCfg.provider,
		"llm_model":          model,
		"credential_name":    adapterCfg.envVar,
		"credential_value":   apiKey,
		"pairing_mode":       false,
	}
	if crewSlug == "blank" {
		body["crew_name"] = "My Crew"
		body["agent_name"] = fmt.Sprintf("%s #1", adapterCfg.label)
	} else {
		body["crew_template_slug"] = crewSlug
	}

	client := newAPIClient()
	resp, err := client.Post("/api/v1/onboarding/setup", body)
	if err != nil {
		return fmt.Errorf("contact server: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	var result struct {
		WorkspaceID string   `json:"workspace_id"`
		CrewID      string   `json:"crew_id"`
		AgentID     string   `json:"agent_id"`
		AgentIDs    []string `json:"agent_ids"`
		AgentCount  int      `json:"agent_count"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	agentCount := max(result.AgentCount, 1)
	agentNoun := "agents"
	if agentCount == 1 {
		agentNoun = "agent"
	}
	cli.PrintSuccess(fmt.Sprintf("Workspace ready — crew %q deployed with %d %s.",
		crewSlug, agentCount, agentNoun,
	))
	if result.AgentID != "" {
		fmt.Printf("First agent ID: %s\n", result.AgentID)
		fmt.Printf("Open it in the browser: %s/crews/agents/%s/chat\n", strings.TrimRight(cliCfg.Server, "/"), result.AgentID)
	}
	return nil
}

func isValidCrewSlug(s string) bool {
	for _, t := range supportedCrewTemplates {
		if t.slug == s {
			return true
		}
	}
	return false
}

func lookupAdapter(key string) (struct {
	key, label, envVar, provider, defaultModel string
}, bool) {
	for _, a := range supportedAdapters {
		if a.key == key {
			return a, true
		}
	}
	return struct {
		key, label, envVar, provider, defaultModel string
	}{}, false
}

// promptCrew renders a numbered list of templates and reads the
// user's choice. Defaults to software-development on bare-Enter.
func promptCrew() (string, error) {
	fmt.Println("Pick your first crew:")
	for i, t := range supportedCrewTemplates {
		fmt.Printf("  %d) %s\n", i+1, t.label)
	}
	fmt.Print("Choice [1]: ")
	var raw string
	if _, err := fmt.Scanln(&raw); err != nil && err.Error() != "unexpected newline" {
		// Empty input → default.
		return supportedCrewTemplates[0].slug, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return supportedCrewTemplates[0].slug, nil
	}
	// Accept either index or slug.
	for i, t := range supportedCrewTemplates {
		if raw == t.slug || raw == fmt.Sprintf("%d", i+1) {
			return t.slug, nil
		}
	}
	return "", fmt.Errorf("invalid choice %q", raw)
}

func promptAdapter() (string, error) {
	fmt.Println("Pick your CLI adapter:")
	for i, a := range supportedAdapters {
		fmt.Printf("  %d) %s\n", i+1, a.label)
	}
	fmt.Print("Choice [1]: ")
	var raw string
	if _, err := fmt.Scanln(&raw); err != nil && err.Error() != "unexpected newline" {
		return supportedAdapters[0].key, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return supportedAdapters[0].key, nil
	}
	for i, a := range supportedAdapters {
		if raw == a.key || raw == fmt.Sprintf("%d", i+1) {
			return a.key, nil
		}
	}
	return "", fmt.Errorf("invalid choice %q", raw)
}

// promptAPIKey reads a CLI token without echo via x/term so the value
// doesn't end up in shell scrollback. Caller passes a friendly
// adapter label ("Claude Code", "Gemini CLI", …) which we drop into
// the prompt so the user understands which CLI to run.
func promptAPIKey(adapterLabel string) (string, error) {
	fmt.Fprintf(os.Stderr, "Paste your %s CLI token (input is hidden): ", adapterLabel)
	bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(bytes)), nil
}

func promptOptional(prompt, defaultValue string) string {
	fmt.Printf("%s: ", prompt)
	var raw string
	if _, err := fmt.Scanln(&raw); err != nil && err.Error() != "unexpected newline" {
		return defaultValue
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue
	}
	return raw
}

// jsonEscape is reserved for future structured prompts that need to
// echo user input back inside JSON without TrimSpace mangling
// (e.g. multi-line system prompts during interactive blank-crew setup).
// Currently unused — kept as scaffolding so the next contributor
// doesn't need to re-add it from scratch.
var _ = json.Marshal
