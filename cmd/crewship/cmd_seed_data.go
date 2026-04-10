package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// ════════════════════════════════════════════════════════════════════════════
// Phase 0: Nuke
// ════════════════════════════════════════════════════════════════════════════

func seedNuke(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Nuking workspace contents...")

	var failures []string

	// Delete issues — page through ALL issues, cancel, then delete.
	// The server caps list limit at 100 per page. Because successful deletes
	// shift the list under us, we always refetch from the head; the
	// safetyCap bounds total iterations so a misbehaving API cannot wedge us
	// in an infinite loop.
	const (
		issuePageSize = 100
		safetyCap     = 10_000
	)
	totalDeleted := 0
	noProgressStreak := 0
pageLoop:
	for iter := 0; iter < safetyCap; iter++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/issues?limit=%d&offset=0", issuePageSize))
		if err != nil {
			failures = append(failures, fmt.Sprintf("list issues: %v", err))
			break pageLoop
		}
		var issues []issueItem
		if err := cli.ReadJSON(resp, &issues); err != nil {
			failures = append(failures, fmt.Sprintf("decode issues: %v", err))
			break pageLoop
		}
		if len(issues) == 0 {
			break pageLoop
		}
		deletedThisPage := 0
		for _, iss := range issues {
			if err := ctx.Err(); err != nil {
				return err
			}
			if iss.Identifier == nil {
				continue
			}
			// Transition to a deletable state (BACKLOG or CANCELLED) by
			// computing a valid path from the issue's CURRENT status.
			// Prefer CANCELLED; fall back to BACKLOG if CANCELLED is unreachable.
			if iss.Status != "BACKLOG" && iss.Status != "CANCELLED" {
				path := seeddata.StatusPathFrom(iss.Status, "CANCELLED")
				if path == nil {
					path = seeddata.StatusPathFrom(iss.Status, "BACKLOG")
				}
				if path == nil {
					failures = append(failures, fmt.Sprintf("no status path from %s for %s", iss.Status, *iss.Identifier))
					fmt.Fprintf(os.Stderr, "  ! nuke %s: no valid transition path from %s\n", *iss.Identifier, iss.Status)
					continue
				}
				transitionFailed := false
				for _, status := range path {
					r, err := client.Patch(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier), map[string]string{"status": status})
					if err != nil {
						failures = append(failures, fmt.Sprintf("transition %s→%s: %v", *iss.Identifier, status, err))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: %v\n", *iss.Identifier, status, err)
						transitionFailed = true
						break
					}
					if r.StatusCode >= 300 {
						failures = append(failures, fmt.Sprintf("transition %s→%s: HTTP %d", *iss.Identifier, status, r.StatusCode))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: HTTP %d\n", *iss.Identifier, status, r.StatusCode)
						r.Body.Close()
						transitionFailed = true
						break
					}
					r.Body.Close()
				}
				if transitionFailed {
					continue
				}
			}
			r, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier))
			if err != nil {
				failures = append(failures, fmt.Sprintf("delete issue %s: %v", *iss.Identifier, err))
				fmt.Fprintf(os.Stderr, "  ! delete issue %s: %v\n", *iss.Identifier, err)
				continue
			}
			if r.StatusCode >= 300 {
				failures = append(failures, fmt.Sprintf("delete issue %s: HTTP %d", *iss.Identifier, r.StatusCode))
				fmt.Fprintf(os.Stderr, "  ! delete issue %s: HTTP %d\n", *iss.Identifier, r.StatusCode)
			} else {
				deletedThisPage++
				totalDeleted++
			}
			r.Body.Close()
		}
		// If a page returned items but we deleted none of them, we'd loop
		// forever (e.g. every issue is un-transitionable). Abort after two
		// consecutive zero-progress iterations.
		if deletedThisPage == 0 {
			noProgressStreak++
			if noProgressStreak >= 2 {
				fmt.Fprintf(os.Stderr, "  ! nuke issues: no progress after %d unreachable items, giving up\n", len(issues))
				break pageLoop
			}
		} else {
			noProgressStreak = 0
		}
	}
	fmt.Fprintf(os.Stderr, "  Deleted %d issues\n", totalDeleted)

	// Delete projects
	if err := nukeList(ctx, client, "/api/v1/projects", "/api/v1/projects/"); err != nil {
		failures = append(failures, fmt.Sprintf("projects: %v", err))
	}

	// Delete labels
	if err := nukeList(ctx, client, "/api/v1/labels", "/api/v1/labels/"); err != nil {
		failures = append(failures, fmt.Sprintf("labels: %v", err))
	}

	// Delete agents (this also removes bindings, credential assignments, skill assignments)
	if err := nukeList(ctx, client, "/api/v1/agents", "/api/v1/agents/"); err != nil {
		failures = append(failures, fmt.Sprintf("agents: %v", err))
	}

	// Delete credentials
	if err := nukeList(ctx, client, "/api/v1/credentials", "/api/v1/credentials/"); err != nil {
		failures = append(failures, fmt.Sprintf("credentials: %v", err))
	}

	// Delete integrations
	if err := nukeCrewIntegrations(ctx, client); err != nil {
		failures = append(failures, fmt.Sprintf("integrations: %v", err))
	}

	// Delete crews
	if err := nukeList(ctx, client, "/api/v1/crews", "/api/v1/crews/"); err != nil {
		failures = append(failures, fmt.Sprintf("crews: %v", err))
	}

	if len(failures) > 0 {
		return fmt.Errorf("workspace cleanup had %d failures: %s", len(failures), strings.Join(failures, "; "))
	}

	cli.PrintSuccess("Workspace contents cleaned")
	return nil
}

func nukeList(ctx context.Context, client *cli.Client, listPath, deletePrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := client.Get(listPath)
	if err != nil {
		return fmt.Errorf("GET %s: %w", listPath, err)
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return fmt.Errorf("decode %s: %w", listPath, err)
	}
	var failures []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := client.Delete(deletePrefix + item.ID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: %v", deletePrefix, item.ID, err))
			continue
		}
		if r.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: HTTP %d", deletePrefix, item.ID, r.StatusCode))
		}
		r.Body.Close()
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d delete failures: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func nukeCrewIntegrations(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := client.Get("/api/v1/integrations/crews")
	if err != nil {
		return fmt.Errorf("GET /api/v1/integrations/crews: %w", err)
	}
	var items []struct {
		ID     string `json:"id"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return fmt.Errorf("decode integrations: %w", err)
	}
	var failures []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/integrations/%s", item.CrewID, item.ID))
		if err != nil {
			failures = append(failures, fmt.Sprintf("DELETE crew %s integration %s: %v", item.CrewID, item.ID, err))
			continue
		}
		if r.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("DELETE crew %s integration %s: HTTP %d", item.CrewID, item.ID, r.StatusCode))
		}
		r.Body.Close()
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d delete failures: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 2: Crews
// ════════════════════════════════════════════════════════════════════════════

func seedCrews(ctx context.Context, client *cli.Client, userID string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "Creating crews...")
	ids := map[string]string{} // slug → id

	for _, c := range seeddata.Crews {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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

func seedAgents(ctx context.Context, client *cli.Client, crewIDs map[string]string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "Creating agents...")
	ids := map[string]string{} // slug → id

	for _, a := range seeddata.Agents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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

func seedSkills(ctx context.Context, client *cli.Client, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if err := ctx.Err(); err != nil {
			return err
		}
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

func seedCredentials(ctx context.Context, client *cli.Client, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Seeding credentials...")

	anthro := seeddata.ResolveAnthropicCredential()
	isReal := os.Getenv("SEED_ANTHROPIC_API_KEY") != ""
	if isReal {
		fmt.Fprintf(os.Stderr, "  Using real %s from SEED_ANTHROPIC_API_KEY\n", anthro.Type)
	} else {
		fmt.Fprintf(os.Stderr, "  WARNING: using demo placeholder key — agents will not work. Set SEED_ANTHROPIC_API_KEY for real credentials.\n")
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
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return resolveByName(client, "/api/v1/credentials", cred.Name)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct{ ID string `json:"id"` }
	if err := cli.ReadJSON(resp, &created); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "  + Credential: %s (%s)\n", cred.Name, cred.Type)
	return created.ID, nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 8–9: Integrations + Bindings
// ════════════════════════════════════════════════════════════════════════════

func seedIntegrations(ctx context.Context, client *cli.Client, crewIDs, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Seeding integrations...")

	// integrationRef tracks both the integration id and the owning crew id,
	// so we only bind agents to integrations within their own crew.
	type integrationRef struct {
		ID     string
		CrewID string
	}
	integrationIDs := map[string]integrationRef{} // integration name → (id, crewID)

	for _, integ := range seeddata.Integrations {
		if err := ctx.Err(); err != nil {
			return err
		}
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
				integrationIDs[integ.Name] = integrationRef{ID: id, CrewID: crewID}
			}
			fmt.Fprintf(os.Stderr, "  = Integration exists: %s\n", integ.DisplayName)
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "  ! Integration %s: HTTP %d\n", integ.Name, resp.StatusCode)
			continue
		}
		var created struct{ ID string `json:"id"` }
		if cli.ReadJSON(resp, &created) == nil {
			integrationIDs[integ.Name] = integrationRef{ID: created.ID, CrewID: crewID}
		}
		fmt.Fprintf(os.Stderr, "  + Integration: %s\n", integ.DisplayName)
	}

	// Create OAuth credentials for integrations (if env vars present)
	oauthCreds := seeddata.ResolveOAuthCredentials()
	oauthCredIDs := map[string]string{} // cred name → id
	for _, oc := range oauthCreds {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		var created struct{ ID string `json:"id"` }
		if cli.ReadJSON(resp, &created) == nil {
			oauthCredIDs[oc.IntegrationName] = created.ID
			status := "ACTIVE"
			if oc.AccessToken == "" {
				status = "PENDING"
			}
			fmt.Fprintf(os.Stderr, "  + OAuth credential: %s (%s)\n", oc.CredName, status)
		}
	}

	// Build a slug → crewSlug lookup so we can scope bindings by crew.
	agentCrewSlugBySlug := map[string]string{}
	for _, a := range seeddata.Agents {
		agentCrewSlugBySlug[a.Slug] = a.CrewSlug
	}

	// Bind agents to integrations. Integrations are crew-scoped, so only
	// bind an agent to integrations that live in the agent's own crew.
	fmt.Fprintln(os.Stderr, "Binding agents to integrations...")
	for _, agentSlug := range seeddata.AgentBindingSlugs {
		if err := ctx.Err(); err != nil {
			return err
		}
		agentID, ok := agentIDs[agentSlug]
		if !ok {
			continue
		}
		agentCrewSlug, ok := agentCrewSlugBySlug[agentSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! agent %q not in seeddata.Agents, skipping bindings\n", agentSlug)
			continue
		}
		agentCrewID, ok := crewIDs[agentCrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! crew %q for agent %s not in crewIDs, skipping bindings\n", agentCrewSlug, agentSlug)
			continue
		}
		for integName, integRef := range integrationIDs {
			if integRef.CrewID != agentCrewID {
				continue // integration belongs to a different crew
			}
			body := map[string]interface{}{
				"mcp_server_id":    integRef.ID,
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

func seedIssues(ctx context.Context, client *cli.Client, crewIDs, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Create labels
	fmt.Fprintln(os.Stderr, "Creating labels...")
	for _, l := range seeddata.Labels {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/projects", map[string]interface{}{
			"name":     p.Name,
			"color":    p.Color,
			"icon":     p.Icon,
			"status":   p.Status,
			"priority": p.Priority,
		})
		if err != nil {
			return fmt.Errorf("project %s: %w", p.Name, err)
		}
		// 409 Conflict → resolve existing.
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			existingID, err := resolveByName(client, "/api/v1/projects", p.Name)
			if err == nil && existingID != "" {
				projectIDs[p.Name] = existingID
				fmt.Fprintf(os.Stderr, "  = Project exists: %s\n", p.Name)
			} else {
				return fmt.Errorf("project %s: conflict but existing record could not be resolved", p.Name)
			}
			continue
		}
		// Any other non-2xx is a real failure.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("project %s: HTTP %d: %s", p.Name, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var created struct{ ID string `json:"id"` }
		if cli.ReadJSON(resp, &created) == nil {
			projectIDs[p.Name] = created.ID
			fmt.Fprintf(os.Stderr, "  + Project: %s\n", p.Name)
		}
	}

	// Create issues — track identifiers and crew IDs for relations.
	// Keyed by stable seed key (def.Title) so relations don't break when
	// individual creations fail and shift positional indexes.
	fmt.Fprintln(os.Stderr, "Creating issues...")
	type createdIssue struct {
		Identifier string
		CrewID     string
	}
	issueByKey := map[string]createdIssue{}

	for _, def := range seeddata.Issues {
		if err := ctx.Err(); err != nil {
			return err
		}
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
			issueByKey[def.Title] = createdIssue{Identifier: ident, CrewID: crewID}
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

	// Create relations between issues using stable seed keys (issue titles).
	// If a referenced issue failed to create, the relation is skipped instead
	// of being wired to the wrong target.
	fmt.Fprintln(os.Stderr, "Creating relations...")
	type relDef struct {
		sourceKey, targetKey, rtype string
	}
	rels := []relDef{
		{"Ping google.com 5 times and save results", "Check HTTP status of 5 popular websites", "blocks"},
		{"Ping google.com 5 times and save results", "Create a directory tree with sample files", "relates_to"},
		{"Trace DNS resolution for 3 domains", "Measure download speed with a 1MB test file", "relates_to"},
		{"Generate a CSV report with random data", "Create a directory tree with sample files", "blocked_by"},
	}
	for _, rd := range rels {
		if err := ctx.Err(); err != nil {
			return err
		}
		src, srcOK := issueByKey[rd.sourceKey]
		tgt, tgtOK := issueByKey[rd.targetKey]
		if !srcOK || !tgtOK {
			fmt.Fprintf(os.Stderr, "  ! relation skipped (missing endpoint): %s %s %s\n", rd.sourceKey, rd.rtype, rd.targetKey)
			continue
		}
		r, err := client.Post(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/relations", src.CrewID, src.Identifier),
			map[string]string{"target_identifier": tgt.Identifier, "relation_type": rd.rtype},
		)
		if err == nil {
			if r.StatusCode < 400 {
				fmt.Fprintf(os.Stderr, "  + %s %s %s\n", src.Identifier, rd.rtype, tgt.Identifier)
			}
			r.Body.Close()
		}
	}

	fmt.Fprintf(os.Stderr, "  Seeded %d labels, %d projects, %d issues\n", len(seeddata.Labels), len(projectIDs), len(seeddata.Issues))
	return nil
}
