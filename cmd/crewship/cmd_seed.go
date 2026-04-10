package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed demo data via the API (replaces prisma/seed.ts)",
	Long: `Creates a complete demo environment: admin user, workspace, crews,
agents with system prompts, credentials, integrations, and sample issues.

On a fresh database, automatically bootstraps the first admin user.
On an existing database, requires authentication (crewship login).

All data is created through the REST API, ensuring business logic
(validation, encryption, audit logging) is properly exercised.`,
	RunE: runSeed,
}

func init() {
	seedCmd.Flags().Bool("nuke", false, "Delete all workspace contents before seeding")
	seedCmd.Flags().Bool("skip-issues", false, "Skip issue/project/label seeding")
	seedCmd.Flags().String("password", "password123", "Admin password for bootstrap")
}

func runSeed(cmd *cobra.Command, args []string) error {
	nuke, _ := cmd.Flags().GetBool("nuke")
	skipIssues, _ := cmd.Flags().GetBool("skip-issues")
	password, _ := cmd.Flags().GetString("password")

	// ── Phase 1: Bootstrap / Auth ──
	client, userID, err := seedBootstrap(password)
	if err != nil {
		return err
	}

	// ── Phase 0: Nuke (after auth, before seed) ──
	if nuke {
		if err := seedNuke(client); err != nil {
			return err
		}
	}

	// ── Phase 2: Crews + Member links ──
	crewIDs, err := seedCrews(client, userID)
	if err != nil {
		return err
	}

	// ── Phase 3: Agents ──
	agentIDs, err := seedAgents(client, crewIDs)
	if err != nil {
		return err
	}

	// ── Phase 4–5: Skills + Assignments ──
	if err := seedSkills(client, agentIDs); err != nil {
		return err
	}

	// ── Phase 6–7: Credentials + Assignments ──
	if err := seedCredentials(client, agentIDs); err != nil {
		return err
	}

	// ── Phase 8–9: Integrations + Bindings ──
	if err := seedIntegrations(client, crewIDs, agentIDs); err != nil {
		return err
	}

	// ── Phase 10: Issues ──
	if !skipIssues {
		if err := seedIssues(client, crewIDs, agentIDs); err != nil {
			return err
		}
	}

	// ── Phase 11: Summary ──
	fmt.Fprintln(os.Stderr, "")
	cli.PrintSuccess(fmt.Sprintf("Seed complete: %d crews, %d agents", len(crewIDs), len(agentIDs)))
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1: Bootstrap
// ════════════════════════════════════════════════════════════════════════════

func seedBootstrap(password string) (*cli.Client, string, error) {
	fmt.Fprintln(os.Stderr, "Bootstrapping...")
	server := cli.ResolveServer(flagServer, cliCfg)

	// Try bootstrap (works only on empty DB)
	unauthClient := cli.NewClient(server, "", "")
	resp, err := unauthClient.Post("/api/v1/bootstrap", map[string]string{
		"email":     "demo@crewship.ai",
		"full_name": "Demo User",
		"password":  password,
	})
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap request failed (is the server running at %s?): %w", server, err)
	}

	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		// Fresh DB — extract token and workspace
		var result struct {
			UserID      string `json:"user_id"`
			WorkspaceID string `json:"workspace_id"`
			CLIToken    string `json:"cli_token"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return nil, "", fmt.Errorf("read bootstrap response: %w", err)
		}

		// Save config for future commands
		cliCfg.Server = server
		cliCfg.Token = result.CLIToken
		cliCfg.Workspace = result.WorkspaceID
		if err := cli.SaveConfig(cliCfg); err != nil {
			cli.PrintWarning("could not save CLI config: " + err.Error())
		}

		fmt.Fprintf(os.Stderr, "  Bootstrapped admin: demo@crewship.ai\n")
		fmt.Fprintf(os.Stderr, "  Workspace: %s\n", result.WorkspaceID)
		return cli.NewClient(server, result.CLIToken, result.WorkspaceID), result.UserID, nil
	}
	// Only fall through to auth for 409 (already initialized).
	// Other errors are real failures.
	if resp.StatusCode != http.StatusConflict {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyStr := strings.TrimSpace(string(bodyBytes))
		if !strings.Contains(bodyStr, "already initialized") {
			return nil, "", fmt.Errorf("bootstrap failed: HTTP %d: %s", resp.StatusCode, bodyStr)
		}
	} else {
		resp.Body.Close()
	}

	// Already initialized — fall back to existing auth
	if err := requireAuth(); err != nil {
		return nil, "", fmt.Errorf("DB already initialized. %w", err)
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", fmt.Errorf("DB already initialized. %w", err)
	}
	fmt.Fprintf(os.Stderr, "  Using existing auth\n")

	// Resolve user ID from CLI token validation
	client := newAPIClient()
	userID := resolveCurrentUserID(client)
	return client, userID, nil
}

// resolveCurrentUserID gets the current user's ID from the CLI token validation endpoint.
func resolveCurrentUserID(client *cli.Client) string {
	resp, err := client.Get("/api/v1/auth/cli-token/validate")
	if err != nil {
		return ""
	}
	var info struct {
		UserID string `json:"user_id"`
	}
	if cli.ReadJSON(resp, &info) == nil {
		return info.UserID
	}
	return ""
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 0: Nuke
// ════════════════════════════════════════════════════════════════════════════

func seedNuke(client *cli.Client) error {
	fmt.Fprintln(os.Stderr, "Nuking workspace contents...")

	// Delete issues — fetch all, cancel, then delete
	resp, err := client.Get("/api/v1/issues?limit=500")
	if err == nil {
		var issues []issueItem
		if cli.ReadJSON(resp, &issues) == nil {
			for _, iss := range issues {
				if iss.Identifier == nil {
					continue
				}
				// Transition through valid status path to CANCELLED (only BACKLOG/CANCELLED can be deleted)
				if iss.Status != "BACKLOG" && iss.Status != "CANCELLED" {
					for _, status := range seeddata.StatusPath("CANCELLED") {
						r, err := client.Patch(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier), map[string]string{"status": status})
						if err != nil {
							fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: %v\n", *iss.Identifier, status, err)
							break
						}
						if r.StatusCode >= 400 {
							fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: HTTP %d\n", *iss.Identifier, status, r.StatusCode)
							r.Body.Close()
							break
						}
						r.Body.Close()
					}
				}
				r, _ := client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier))
				if r != nil {
					r.Body.Close()
				}
			}
			fmt.Fprintf(os.Stderr, "  Deleted %d issues\n", len(issues))
		}
	}

	// Delete projects
	nukeList(client, "/api/v1/projects", "/api/v1/projects/")

	// Delete labels
	nukeList(client, "/api/v1/labels", "/api/v1/labels/")

	// Delete agents (this also removes bindings, credential assignments, skill assignments)
	nukeList(client, "/api/v1/agents", "/api/v1/agents/")

	// Delete credentials
	nukeList(client, "/api/v1/credentials", "/api/v1/credentials/")

	// Delete integrations
	nukeCrewIntegrations(client)

	// Delete crews
	nukeList(client, "/api/v1/crews", "/api/v1/crews/")

	cli.PrintSuccess("Workspace contents cleaned")
	return nil
}

func nukeList(client *cli.Client, listPath, deletePrefix string) {
	resp, err := client.Get(listPath)
	if err != nil {
		return
	}
	var items []struct {
		ID string `json:"id"`
	}
	if cli.ReadJSON(resp, &items) == nil {
		for _, item := range items {
			r, _ := client.Delete(deletePrefix + item.ID)
			if r != nil {
				r.Body.Close()
			}
		}
	}
}

func nukeCrewIntegrations(client *cli.Client) {
	resp, err := client.Get("/api/v1/integrations/crews")
	if err != nil {
		return
	}
	var items []struct {
		ID     string `json:"id"`
		CrewID string `json:"crew_id"`
	}
	if cli.ReadJSON(resp, &items) == nil {
		for _, item := range items {
			r, _ := client.Delete(fmt.Sprintf("/api/v1/crews/%s/integrations/%s", item.CrewID, item.ID))
			if r != nil {
				r.Body.Close()
			}
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 2: Crews
// ════════════════════════════════════════════════════════════════════════════

func seedCrews(client *cli.Client, userID string) (map[string]string, error) {
	fmt.Fprintln(os.Stderr, "Creating crews...")
	ids := map[string]string{} // slug → id

	for _, c := range seeddata.Crews {
		body := map[string]string{
			"name":  c.Name,
			"slug":  c.Slug,
			"color": c.Color,
			"icon":  c.Icon,
		}
		id, err := createOrResolve(client, "/api/v1/crews", body, "/api/v1/crews", c.Slug)
		if err != nil {
			return nil, fmt.Errorf("crew %s: %w", c.Slug, err)
		}
		ids[c.Slug] = id
		fmt.Fprintf(os.Stderr, "  + Crew: %s (%s)\n", c.Name, id[:8])

		// Add current user as crew member
		if userID != "" {
			r, err := client.Post(
				fmt.Sprintf("/api/v1/crews/%s/members", id),
				map[string]string{"user_id": userID},
			)
			if err == nil {
				r.Body.Close()
			}
		}
	}
	if userID != "" {
		fmt.Fprintf(os.Stderr, "  Linked user to %d crews\n", len(ids))
	}
	return ids, nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 3: Agents
// ════════════════════════════════════════════════════════════════════════════

func seedAgents(client *cli.Client, crewIDs map[string]string) (map[string]string, error) {
	fmt.Fprintln(os.Stderr, "Creating agents...")
	ids := map[string]string{} // slug → id

	for _, a := range seeddata.Agents {
		crewID, ok := crewIDs[a.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Crew %q not found, skipping agent %s\n", a.CrewSlug, a.Name)
			continue
		}
		prompt := seeddata.AgentPrompt(a.PromptSlug)
		body := map[string]interface{}{
			"name":            a.Name,
			"slug":            a.Slug,
			"crew_id":         crewID,
			"role_title":      a.RoleTitle,
			"agent_role":      a.AgentRole,
			"cli_adapter":     a.CLIAdapter,
			"llm_provider":    a.LLMProvider,
			"llm_model":       a.LLMModel,
			"tool_profile":    a.ToolProfile,
			"timeout_seconds": a.TimeoutSeconds,
			"memory_enabled":  a.MemoryEnabled,
			"system_prompt":   prompt,
		}
		id, err := createOrResolve(client, "/api/v1/agents", body, "/api/v1/agents", a.Slug)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", a.Slug, err)
		}
		ids[a.Slug] = id
		fmt.Fprintf(os.Stderr, "  + Agent: %s (%s, %s)\n", a.Name, a.AgentRole, a.ToolProfile)
	}
	return ids, nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 4–5: Skills + Assignments
// ════════════════════════════════════════════════════════════════════════════

func seedSkills(client *cli.Client, agentIDs map[string]string) error {
	fmt.Fprintln(os.Stderr, "Seeding skills...")

	// Fetch existing skills (bundled ones are auto-seeded on server startup)
	skillIDs := map[string]string{} // slug → id
	resp, err := client.Get("/api/v1/skills")
	if err == nil {
		var existing []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if cli.ReadJSON(resp, &existing) == nil {
			for _, s := range existing {
				skillIDs[s.Slug] = s.ID
			}
		}
	}

	// Create missing skills via import endpoint (SKILL.md format)
	for _, s := range seeddata.Skills {
		if _, exists := skillIDs[s.Slug]; exists {
			fmt.Fprintf(os.Stderr, "  = Skill exists: %s\n", s.Name)
			continue
		}
		// The import endpoint expects SKILL.md format with YAML frontmatter
		wsID := client.GetWorkspaceID()
		importPath := fmt.Sprintf("/api/v1/workspaces/%s/skills/import", wsID)
		body := map[string]string{"content": s.SkillMD()}
		resp, err := client.Post(importPath, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Skill %s: %v\n", s.Name, err)
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "  ! Skill %s: HTTP %d\n", s.Name, resp.StatusCode)
			continue
		}
		var created struct {
			SkillID string `json:"skill_id"`
			Slug    string `json:"slug"`
		}
		if cli.ReadJSON(resp, &created) == nil {
			skillIDs[s.Slug] = created.SkillID
		}
		fmt.Fprintf(os.Stderr, "  + Skill: %s\n", s.Name)
	}

	// Assign skills to agents
	fmt.Fprintln(os.Stderr, "Assigning skills...")
	for agentSlug, skillSlugs := range seeddata.SkillAssignments {
		agentID, ok := agentIDs[agentSlug]
		if !ok {
			continue
		}
		for _, skillSlug := range skillSlugs {
			skillID, ok := skillIDs[skillSlug]
			if !ok {
				fmt.Fprintf(os.Stderr, "  ! Skill %q not found for agent %s\n", skillSlug, agentSlug)
				continue
			}
			resp, err := client.Post(
				fmt.Sprintf("/api/v1/agents/%s/skills", agentID),
				map[string]string{"skill_id": skillID},
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Assign %s→%s: %v\n", agentSlug, skillSlug, err)
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusConflict {
				continue // already assigned
			}
		}
		fmt.Fprintf(os.Stderr, "  + %s: %s\n", agentSlug, strings.Join(skillSlugs, ", "))
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 6–7: Credentials + Assignments
// ════════════════════════════════════════════════════════════════════════════

func seedCredentials(client *cli.Client, agentIDs map[string]string) error {
	fmt.Fprintln(os.Stderr, "Seeding credentials...")

	anthro := seeddata.ResolveAnthropicCredential()
	isReal := os.Getenv("SEED_ANTHROPIC_API_KEY") != ""
	if isReal {
		fmt.Fprintf(os.Stderr, "  Using real %s from SEED_ANTHROPIC_API_KEY\n", anthro.Type)
	} else {
		fmt.Fprintf(os.Stderr, "  Using demo placeholder (set SEED_ANTHROPIC_API_KEY for real agents)\n")
	}

	anthroID, err := seedOneCredential(client, anthro)
	if err != nil {
		return fmt.Errorf("anthropic credential: %w", err)
	}

	// Assign to all agents
	for _, agentID := range agentIDs {
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/agents/%s/credentials", agentID),
			map[string]string{"credential_id": anthroID, "env_var_name": anthro.EnvVarName},
		)
		if err == nil {
			resp.Body.Close()
		}
	}
	fmt.Fprintf(os.Stderr, "  + Assigned %s to %d agents\n", anthro.Name, len(agentIDs))

	// Google credential (optional)
	googleCred := seeddata.ResolveGoogleCredential()
	if googleCred != nil {
		googleID, err := seedOneCredential(client, *googleCred)
		if err != nil {
			cli.PrintWarning("Google credential: " + err.Error())
		} else {
			for _, agentID := range agentIDs {
				resp, err := client.Post(
					fmt.Sprintf("/api/v1/agents/%s/credentials", agentID),
					map[string]string{"credential_id": googleID, "env_var_name": googleCred.EnvVarName},
				)
				if err == nil {
					resp.Body.Close()
				}
			}
			fmt.Fprintf(os.Stderr, "  + Assigned %s to %d agents\n", googleCred.Name, len(agentIDs))
		}
	} else {
		fmt.Fprintln(os.Stderr, "  Skipping Google credential (set SEED_GOOGLE_EMAIL + SEED_GOOGLE_PASSWORD)")
	}

	return nil
}

func seedOneCredential(client *cli.Client, cred seeddata.CredentialDef) (string, error) {
	// Check if credential already exists first
	existingID, err := resolveByName(client, "/api/v1/credentials", cred.Name)
	if err == nil && existingID != "" {
		fmt.Fprintf(os.Stderr, "  = Credential exists: %s\n", cred.Name)
		return existingID, nil
	}

	body := map[string]string{
		"name":        cred.Name,
		"description": cred.Description,
		"type":        cred.Type,
		"provider":    cred.Provider,
		"value":       cred.Value,
		"scope":       "WORKSPACE",
	}
	resp, err := client.Post("/api/v1/credentials", body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusInternalServerError {
		resp.Body.Close()
		// May already exist (API returns 500 on UNIQUE constraint instead of 409)
		return resolveByName(client, "/api/v1/credentials", cred.Name)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &created); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "  + Credential: %s (%s)\n", cred.Name, cred.Type)
	return created.ID, nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 8–9: Integrations + Bindings
// ════════════════════════════════════════════════════════════════════════════

func seedIntegrations(client *cli.Client, crewIDs, agentIDs map[string]string) error {
	fmt.Fprintln(os.Stderr, "Seeding integrations...")

	integrationIDs := map[string]string{} // integration name → id

	for _, integ := range seeddata.Integrations {
		crewID, ok := crewIDs[integ.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Crew %q not found for integration %s\n", integ.CrewSlug, integ.Name)
			continue
		}

		body := map[string]interface{}{
			"name":         integ.Name,
			"display_name": integ.DisplayName,
			"transport":    integ.Transport,
		}
		if integ.Endpoint != "" {
			body["endpoint"] = integ.Endpoint
		}
		if integ.Command != "" {
			body["command"] = integ.Command
		}
		if integ.ArgsJSON != "" {
			body["args_json"] = integ.ArgsJSON
		}
		if integ.EnvJSON != "" {
			body["env_json"] = integ.EnvJSON
		}

		path := fmt.Sprintf("/api/v1/crews/%s/integrations", crewID)
		resp, err := client.Post(path, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Integration %s: %v\n", integ.Name, err)
			continue
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			// Resolve existing
			id, err := resolveCrewIntegration(client, crewID, integ.Name)
			if err == nil {
				integrationIDs[integ.Name] = id
			}
			fmt.Fprintf(os.Stderr, "  = Integration exists: %s\n", integ.DisplayName)
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "  ! Integration %s: HTTP %d\n", integ.Name, resp.StatusCode)
			continue
		}
		var created struct {
			ID string `json:"id"`
		}
		if cli.ReadJSON(resp, &created) == nil {
			integrationIDs[integ.Name] = created.ID
		}
		fmt.Fprintf(os.Stderr, "  + Integration: %s\n", integ.DisplayName)
	}

	// Create OAuth credentials for integrations (if env vars present)
	oauthCreds := seeddata.ResolveOAuthCredentials()
	oauthCredIDs := map[string]string{} // cred name → id
	for _, oc := range oauthCreds {
		body := map[string]interface{}{
			"name":                oc.CredName,
			"type":                "OAUTH2",
			"value":               oc.AccessToken,
			"oauth_client_id":     oc.OAuthClientID,
			"oauth_client_secret": oc.OAuthClientSecret,
			"oauth_auth_url":      oc.OAuthAuthURL,
			"oauth_token_url":     oc.OAuthTokenURL,
			"oauth_scopes":        oc.OAuthScopes,
		}
		resp, err := client.Post("/api/v1/credentials", body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! OAuth credential %s: %v\n", oc.CredName, err)
			continue
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			id, _ := resolveByName(client, "/api/v1/credentials", oc.CredName)
			if id != "" {
				oauthCredIDs[oc.IntegrationName] = id
			}
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			continue
		}
		var created struct {
			ID string `json:"id"`
		}
		if cli.ReadJSON(resp, &created) == nil {
			oauthCredIDs[oc.IntegrationName] = created.ID
			status := "ACTIVE"
			if oc.AccessToken == "" {
				status = "PENDING"
			}
			fmt.Fprintf(os.Stderr, "  + OAuth credential: %s (%s)\n", oc.CredName, status)
		}
	}

	// Bind agents to integrations
	fmt.Fprintln(os.Stderr, "Binding agents to integrations...")
	for _, agentSlug := range seeddata.AgentBindingSlugs {
		agentID, ok := agentIDs[agentSlug]
		if !ok {
			continue
		}
		for integName, integID := range integrationIDs {
			body := map[string]interface{}{
				"mcp_server_id":    integID,
				"mcp_server_scope": "crew",
				"cred_type":        "bearer",
				"enabled":          true,
			}
			if credID, ok := oauthCredIDs[integName]; ok {
				body["credential_id"] = credID
			}
			resp, err := client.Post(
				fmt.Sprintf("/api/v1/agents/%s/integrations", agentID),
				body,
			)
			if err == nil {
				resp.Body.Close()
			}
		}
	}
	if len(integrationIDs) > 0 {
		fmt.Fprintf(os.Stderr, "  + Bound %d agents to %d integrations\n", len(seeddata.AgentBindingSlugs), len(integrationIDs))
	}

	return nil
}

func resolveCrewIntegration(client *cli.Client, crewID, name string) (string, error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/integrations", crewID))
	if err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Name == name {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("integration %q not found in crew %s", name, crewID)
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 10: Issues
// ════════════════════════════════════════════════════════════════════════════

func seedIssues(client *cli.Client, crewIDs, agentIDs map[string]string) error {
	// Create labels
	fmt.Fprintln(os.Stderr, "Creating labels...")
	for _, l := range seeddata.Labels {
		resp, err := client.Post("/api/v1/labels", l)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Label %s: %v\n", l.Name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			fmt.Fprintf(os.Stderr, "  + Label: %s\n", l.Name)
		}
	}

	// Create projects
	fmt.Fprintln(os.Stderr, "Creating projects...")
	projectIDs := map[string]string{} // name → id
	for _, p := range seeddata.Projects {
		resp, err := client.Post("/api/v1/projects", map[string]interface{}{
			"name":     p.Name,
			"color":    p.Color,
			"icon":     p.Icon,
			"status":   p.Status,
			"priority": p.Priority,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Project %s: %v\n", p.Name, err)
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			// Resolve existing project by name (same pattern as credentials)
			existingID, err := resolveByName(client, "/api/v1/projects", p.Name)
			if err == nil && existingID != "" {
				projectIDs[p.Name] = existingID
				fmt.Fprintf(os.Stderr, "  = Project exists: %s\n", p.Name)
			} else {
				fmt.Fprintf(os.Stderr, "  ! Project %s: HTTP %d (could not resolve existing)\n", p.Name, resp.StatusCode)
			}
			continue
		}
		var created struct {
			ID string `json:"id"`
		}
		if cli.ReadJSON(resp, &created) == nil {
			projectIDs[p.Name] = created.ID
			fmt.Fprintf(os.Stderr, "  + Project: %s\n", p.Name)
		}
	}

	// Create issues — track identifiers and crew IDs for relations
	fmt.Fprintln(os.Stderr, "Creating issues...")
	type createdIssue struct {
		Identifier string
		CrewID     string
	}
	createdIssues := make([]createdIssue, 0, len(seeddata.Issues))

	for _, def := range seeddata.Issues {
		crewID, ok := crewIDs[def.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Crew %q not found, skipping: %s\n", def.CrewSlug, def.Title)
			continue
		}

		body := map[string]interface{}{
			"title":    def.Title,
			"priority": def.Priority,
		}
		if def.Description != "" {
			body["description"] = def.Description
		}
		if def.Project != "" {
			if pid, ok := projectIDs[def.Project]; ok {
				body["project_id"] = pid
			}
		}
		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues", crewID), body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", def.Title, err)
			continue
		}
		if err := cli.CheckError(resp); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", def.Title, err)
			continue
		}
		var created struct {
			ID         string  `json:"id"`
			Identifier *string `json:"identifier"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			continue
		}
		ident := ""
		if created.Identifier != nil {
			ident = *created.Identifier
		}
		if ident != "" {
			createdIssues = append(createdIssues, createdIssue{Identifier: ident, CrewID: crewID})
		}
		fmt.Fprintf(os.Stderr, "  + %s: %s (%s)\n", ident, truncate(def.Title, 50), def.Priority)

		// Transition to target state
		if def.TargetState != "" && def.TargetState != "BACKLOG" && ident != "" {
			for _, status := range seeddata.StatusPath(def.TargetState) {
				r, err := client.Patch(
					fmt.Sprintf("/api/v1/crews/%s/issues/%s", crewID, ident),
					map[string]string{"status": status},
				)
				if err != nil {
					break
				}
				r.Body.Close()
				if r.StatusCode >= 400 {
					break
				}
			}
		}

		// Assign agent via PATCH
		if def.Assignee != "" && ident != "" {
			aid, ok := agentIDs[def.Assignee]
			if ok {
				r, err := client.Patch(
					fmt.Sprintf("/api/v1/crews/%s/issues/%s", crewID, ident),
					map[string]string{"assignee_type": "agent", "assignee_id": aid},
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "    ! assign %s→%s: %v\n", ident, def.Assignee, err)
				} else {
					if r.StatusCode >= 400 {
						fmt.Fprintf(os.Stderr, "    ! assign %s→%s: HTTP %d\n", ident, def.Assignee, r.StatusCode)
					}
					r.Body.Close()
				}
			} else {
				fmt.Fprintf(os.Stderr, "    ! agent %q not in agentIDs\n", def.Assignee)
			}
		}

		// Add comment
		if def.Comment != "" && ident != "" {
			r, err := client.Post(
				fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments", crewID, ident),
				map[string]string{"body": def.Comment},
			)
			if err == nil {
				r.Body.Close()
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	// Create relations between issues using tracked identifiers
	fmt.Fprintln(os.Stderr, "Creating relations...")
	if len(createdIssues) >= 6 {
		type relDef struct {
			source, target, rtype string
		}
		rels := []relDef{
			{createdIssues[0].Identifier, createdIssues[1].Identifier, "blocks"},
			{createdIssues[0].Identifier, createdIssues[4].Identifier, "relates_to"},
			{createdIssues[2].Identifier, createdIssues[3].Identifier, "relates_to"},
			{createdIssues[5].Identifier, createdIssues[4].Identifier, "blocked_by"},
		}
		// Build identifier→crewID lookup from tracked data
		issueCrew := map[string]string{}
		for _, ci := range createdIssues {
			issueCrew[ci.Identifier] = ci.CrewID
		}
		for _, rd := range rels {
			srcCrewID := issueCrew[rd.source]
			if srcCrewID == "" {
				continue
			}
			r, err := client.Post(
				fmt.Sprintf("/api/v1/crews/%s/issues/%s/relations", srcCrewID, rd.source),
				map[string]string{"target_identifier": rd.target, "relation_type": rd.rtype},
			)
			if err == nil {
				if r.StatusCode < 400 {
					fmt.Fprintf(os.Stderr, "  + %s %s %s\n", rd.source, rd.rtype, rd.target)
				}
				r.Body.Close()
			}
		}
	}

	fmt.Fprintf(os.Stderr, "  Seeded %d labels, %d projects, %d issues\n", len(seeddata.Labels), len(projectIDs), len(seeddata.Issues))
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Helpers
// ════════════════════════════════════════════════════════════════════════════

// createOrResolve creates a resource via POST. On 409, resolves existing by slug.
func createOrResolve(client *cli.Client, createPath string, body interface{}, listPath, slug string) (string, error) {
	resp, err := client.Post(createPath, body)
	if err != nil {
		return "", err
	}
	// Handle both 409 Conflict and 500 (some APIs return 500 on UNIQUE constraint).
	// TODO(tech-debt): treating HTTP 500 as a conflict is a deliberate workaround
	// for API inconsistency — some endpoints do not return 409 on duplicate inserts.
	// Same rationale as seedOneCredential. Remove once all endpoints return 409.
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusInternalServerError {
		resp.Body.Close()
		return resolveBySlug(client, listPath, slug)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// resolveBySlug lists resources and finds one by slug.
func resolveBySlug(client *cli.Client, listPath, slug string) (string, error) {
	resp, err := client.Get(listPath)
	if err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Slug == slug {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("resource with slug %q not found", slug)
}

// resolveByName lists resources and finds one by name.
func resolveByName(client *cli.Client, listPath, name string) (string, error) {
	resp, err := client.Get(listPath)
	if err != nil {
		return "", err
	}
	// Parse as raw JSON to handle both "name" and "slug" keys
	var raw []byte
	raw, err = readBody(resp)
	if err != nil {
		return "", err
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if n, ok := item["name"].(string); ok && n == name {
			if id, ok := item["id"].(string); ok {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("resource with name %q not found", name)
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
