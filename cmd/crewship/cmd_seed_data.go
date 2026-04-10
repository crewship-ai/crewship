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

	// Delete issues — paginate through the full result set. A single
	// limit=500 request would leave any issue past the first page behind
	// and block later project/crew deletion.
	const pageLimit = 500
	totalDeleted := 0
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/issues?limit=%d&offset=%d", pageLimit, offset))
		if err != nil {
			failures = append(failures, fmt.Sprintf("list issues (offset=%d): %v", offset, err))
			break
		}
		var issues []issueItem
		if err := cli.ReadJSON(resp, &issues); err != nil {
			failures = append(failures, fmt.Sprintf("decode issues (offset=%d): %v", offset, err))
			break
		}
		if len(issues) == 0 {
			break
		}
		deletedOnPage := 0
		for _, iss := range issues {
			if err := ctx.Err(); err != nil {
				return err
			}
			if iss.Identifier == nil {
				continue
			}
			// Transition through a valid status path from the issue's CURRENT
			// state to CANCELLED (only BACKLOG/CANCELLED can be deleted).
			// Using StatusPath("CANCELLED") would always start from BACKLOG,
			// which for an issue already in TODO/IN_PROGRESS/DONE would emit
			// a backward transition the server rejects (e.g. IN_PROGRESS→TODO
			// on its way to BACKLOG→CANCELLED), leaving the issue non-deletable.
			if iss.Status != "BACKLOG" && iss.Status != "CANCELLED" {
				path := seeddata.StatusPathFrom(iss.Status, "CANCELLED")
				if path == nil {
					failures = append(failures, fmt.Sprintf("no transition path %s→CANCELLED for %s", iss.Status, *iss.Identifier))
					fmt.Fprintf(os.Stderr, "  ! nuke: no transition path %s→CANCELLED for %s\n", iss.Status, *iss.Identifier)
					continue
				}
				for _, status := range path {
					r, err := client.Patch(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier), map[string]string{"status": status})
					if err != nil {
						failures = append(failures, fmt.Sprintf("transition %s→%s: %v", *iss.Identifier, status, err))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: %v\n", *iss.Identifier, status, err)
						break
					}
					if r.StatusCode >= 300 {
						failures = append(failures, fmt.Sprintf("transition %s→%s: HTTP %d", *iss.Identifier, status, r.StatusCode))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: HTTP %d\n", *iss.Identifier, status, r.StatusCode)
						r.Body.Close()
						break
					}
					r.Body.Close()
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
				r.Body.Close()
				continue
			}
			r.Body.Close()
			totalDeleted++
			deletedOnPage++
		}
		// End conditions:
		// - Partial page (fewer than pageLimit rows) → nothing left to scan.
		// - Full page but zero deletions → every row is undeletable; advance
		//   offset past them so we don't re-fetch the same 500 rows forever.
		// - Full page with deletions → the rows we removed shifted the
		//   result set, so the next page starts at the same offset (0).
		if len(issues) < pageLimit {
			break
		}
		if deletedOnPage == 0 {
			fmt.Fprintf(os.Stderr, "  ! nuke: page at offset=%d had no deletable issues, advancing\n", offset)
			offset += pageLimit
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
	linked := 0

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

		// Add current user as crew member. Treat 409 Conflict as idempotent
		// (already a member); surface every other failure so the summary line
		// below doesn't over-report.
		if userID != "" {
			r, err := client.Post(
				fmt.Sprintf("/api/v1/crews/%s/members", id),
				map[string]string{"user_id": userID},
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Link user to crew %s: %v\n", c.Slug, err)
				continue
			}
			if r.StatusCode >= 400 && r.StatusCode != http.StatusConflict {
				fmt.Fprintf(os.Stderr, "  ! Link user to crew %s: HTTP %d\n", c.Slug, r.StatusCode)
				r.Body.Close()
				continue
			}
			r.Body.Close()
			linked++
		}
	}
	if userID != "" {
		fmt.Fprintf(os.Stderr, "  Linked user to %d/%d crews\n", linked, len(ids))
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

	// Assign skills to agents. Treat 409 Conflict as idempotent (already
	// assigned); surface every other non-2xx so the per-agent summary below
	// only lists skills that were actually linked.
	fmt.Fprintln(os.Stderr, "Assigning skills...")
	for agentSlug, skillSlugs := range seeddata.SkillAssignments {
		if err := ctx.Err(); err != nil {
			return err
		}
		agentID, ok := agentIDs[agentSlug]
		if !ok {
			continue
		}
		assigned := make([]string, 0, len(skillSlugs))
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
			status := resp.StatusCode
			resp.Body.Close()
			if status == http.StatusConflict {
				assigned = append(assigned, skillSlug) // already assigned — still a valid end-state
				continue
			}
			if status >= 400 {
				fmt.Fprintf(os.Stderr, "  ! Assign %s→%s: HTTP %d\n", agentSlug, skillSlug, status)
				continue
			}
			assigned = append(assigned, skillSlug)
		}
		if len(assigned) > 0 {
			fmt.Fprintf(os.Stderr, "  + %s: %s\n", agentSlug, strings.Join(assigned, ", "))
		}
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

	// Assign to all agents. Treat 409 Conflict as idempotent; surface other
	// failures so the summary line reflects only successful assignments.
	assigned := 0
	for slug, agentID := range agentIDs {
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/agents/%s/credentials", agentID),
			map[string]string{"credential_id": anthroID, "env_var_name": anthro.EnvVarName},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Assign credential to agent %s: %v\n", slug, err)
			continue
		}
		status := resp.StatusCode
		resp.Body.Close()
		if status >= 400 && status != http.StatusConflict {
			fmt.Fprintf(os.Stderr, "  ! Assign credential to agent %s: HTTP %d\n", slug, status)
			continue
		}
		assigned++
	}
	fmt.Fprintf(os.Stderr, "  + Assigned %s to %d/%d agents\n", anthro.Name, assigned, len(agentIDs))

	// Google credential (optional). Same idempotent/surface-failure pattern
	// as the Anthropic assignment above — treat 409 as already linked,
	// report only genuinely successful assignments in the summary.
	googleCred := seeddata.ResolveGoogleCredential()
	if googleCred != nil {
		googleID, err := seedOneCredential(client, *googleCred)
		if err != nil {
			cli.PrintWarning("Google credential: " + err.Error())
		} else {
			googleAssigned := 0
			for slug, agentID := range agentIDs {
				resp, err := client.Post(
					fmt.Sprintf("/api/v1/agents/%s/credentials", agentID),
					map[string]string{"credential_id": googleID, "env_var_name": googleCred.EnvVarName},
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ! Assign Google credential to agent %s: %v\n", slug, err)
					continue
				}
				status := resp.StatusCode
				resp.Body.Close()
				if status >= 400 && status != http.StatusConflict {
					fmt.Fprintf(os.Stderr, "  ! Assign Google credential to agent %s: HTTP %d\n", slug, status)
					continue
				}
				googleAssigned++
			}
			fmt.Fprintf(os.Stderr, "  + Assigned %s to %d/%d agents\n", googleCred.Name, googleAssigned, len(agentIDs))
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

func seedIntegrations(ctx context.Context, client *cli.Client, crewIDs, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Seeding integrations...")

	// Crew-scoped integration map: crewID → (integration name → id).
	// A flat name→id map would silently collide when two crews share an
	// integration name (which is legal — names are unique per crew, not per
	// workspace) and would also let the binding loop wire agents up to
	// integrations that belong to other crews.
	integrationIDs := map[string]map[string]string{}
	addIntegration := func(crewID, name, id string) {
		if integrationIDs[crewID] == nil {
			integrationIDs[crewID] = map[string]string{}
		}
		integrationIDs[crewID][name] = id
	}

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
			// Resolve existing — only treat the conflict as recovered if the
			// follow-up lookup actually returns an ID. Otherwise integrationIDs
			// is left without an entry and later binding code will silently
			// skip this integration, so we surface it as a failure instead.
			id, err := resolveCrewIntegration(client, crewID, integ.Name)
			if err != nil || id == "" {
				fmt.Fprintf(os.Stderr, "  ! Integration %s: 409 conflict but lookup failed: %v\n", integ.Name, err)
				continue
			}
			addIntegration(crewID, integ.Name, id)
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
		// Report parse failures — otherwise the integration exists server-side
		// but isn't tracked in integrationIDs, so the bindings loop below
		// silently skips it while the success line still prints.
		if err := cli.ReadJSON(resp, &created); err != nil {
			fmt.Fprintf(os.Stderr, "  ! Integration %s: parse response: %v\n", integ.Name, err)
			continue
		}
		if created.ID == "" {
			fmt.Fprintf(os.Stderr, "  ! Integration %s: response missing id\n", integ.Name)
			continue
		}
		addIntegration(crewID, integ.Name, created.ID)
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
			// Same caveat as the integration conflict above: a 409 is only
			// a recovered state if we actually find the existing credential.
			// Otherwise bindings below would silently miss the credential_id,
			// so surface the failure explicitly.
			id, err := resolveByName(client, "/api/v1/credentials", oc.CredName)
			if err != nil || id == "" {
				fmt.Fprintf(os.Stderr, "  ! OAuth credential %s: 409 conflict but lookup failed: %v\n", oc.CredName, err)
				continue
			}
			oauthCredIDs[oc.IntegrationName] = id
			continue
		}
		if resp.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "  ! OAuth credential %s: HTTP %d\n", oc.CredName, resp.StatusCode)
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

	// Bind agents to integrations. Treat 409 Conflict as idempotent; surface
	// every other failure so the summary line doesn't claim successful
	// bindings that never happened.
	//
	// Bindings are scoped to the agent's own crew — otherwise an agent in
	// crew A could end up bound to an integration provisioned for crew B,
	// which is both a scope leak and a server-side permission error waiting
	// to happen. Build a slug → crewID lookup from the static seed data so
	// we can resolve each agent's crew without an extra API round-trip.
	agentCrewIDBySlug := map[string]string{}
	for _, a := range seeddata.Agents {
		if cid, ok := crewIDs[a.CrewSlug]; ok {
			agentCrewIDBySlug[a.Slug] = cid
		}
	}

	fmt.Fprintln(os.Stderr, "Binding agents to integrations...")
	boundAgents := 0
	totalBindings := 0
	successBindings := 0
	for _, agentSlug := range seeddata.AgentBindingSlugs {
		if err := ctx.Err(); err != nil {
			return err
		}
		agentID, ok := agentIDs[agentSlug]
		if !ok {
			continue
		}
		crewID, ok := agentCrewIDBySlug[agentSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Bind %s: crew not found, skipping\n", agentSlug)
			continue
		}
		perAgentSuccess := 0
		for integName, integID := range integrationIDs[crewID] {
			totalBindings++
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
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Bind %s→%s: %v\n", agentSlug, integName, err)
				continue
			}
			status := resp.StatusCode
			resp.Body.Close()
			if status >= 400 && status != http.StatusConflict {
				fmt.Fprintf(os.Stderr, "  ! Bind %s→%s: HTTP %d\n", agentSlug, integName, status)
				continue
			}
			successBindings++
			perAgentSuccess++
		}
		if perAgentSuccess > 0 {
			boundAgents++
		}
	}
	if totalBindings > 0 {
		fmt.Fprintf(os.Stderr, "  + Bound %d agents, %d/%d bindings succeeded\n", boundAgents, successBindings, totalBindings)
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
		var created struct {
			ID string `json:"id"`
		}
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
