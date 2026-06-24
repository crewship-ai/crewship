package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// ════════════════════════════════════════════════════════════════════════════
// Phase 0: Nuke
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
		body := map[string]interface{}{
			"name":  c.Name,
			"slug":  c.Slug,
			"color": c.Color,
			"icon":  c.Icon,
		}
		if c.RuntimeImage != "" {
			body["runtime_image"] = c.RuntimeImage
		}
		if c.DevcontainerConfig != "" {
			body["devcontainer_config"] = c.DevcontainerConfig
		}
		if c.MiseConfig != "" {
			body["mise_config"] = c.MiseConfig
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

// seedCrewConnections POSTs a bidirectional connection for every unordered
// crew pair so cross-crew task delegation works out of the box.
//
// Why this exists: the mission planner happily produces tasks whose
// assigned_agent_id lives in a different crew than the mission's owning
// crew (observed on dev1 with DEV-4 — a devops mission delegated a step
// to a quality agent). At dispatch time mission_tasks.go:385 refuses the
// hand-off unless crew_connections has an active row joining the two
// crews. The old seed never wrote that row, so every cross-crew task
// failed with "crew X is not connected to crew Y — create a crew
// connection first" before the agent ever ran.
//
// We seed all-pairs because the demo workspace has only four crews
// (C(4,2)=6 rows) and the planner has no advance notice which pairs the
// LEAD will reach for. Production deployments can prune via the
// /api/v1/crew-connections DELETE endpoint or the `crewship crew
// connection rm` CLI if a strict policy is desired.
func seedCrewConnections(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(crewIDs) < 2 {
		return nil
	}
	fmt.Fprintln(os.Stderr, "Connecting crews (all-pairs, bidirectional)...")

	// Deterministic ordering so the resulting (from, to) tuples are
	// stable across re-runs and CI snapshots.
	slugs := make([]string, 0, len(crewIDs))
	for s := range crewIDs {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)

	created, skipped := 0, 0
	for i := 0; i < len(slugs); i++ {
		for j := i + 1; j < len(slugs); j++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			fromSlug, toSlug := slugs[i], slugs[j]
			body := map[string]string{
				"from_crew_id": crewIDs[fromSlug],
				"to_crew_id":   crewIDs[toSlug],
				"direction":    "bidirectional",
			}
			resp, err := client.Post("/api/v1/crew-connections", body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Connect %s↔%s: %v\n", fromSlug, toSlug, err)
				continue
			}
			// 409 = already exists (idempotent re-seed); anything else
			// outside the 2xx success band is unexpected and gets
			// surfaced so a real misconfiguration doesn't go silent.
			switch {
			case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
				created++
				fmt.Fprintf(os.Stderr, "  + Connection: %s ↔ %s\n", fromSlug, toSlug)
			case resp.StatusCode == http.StatusConflict:
				skipped++
			default:
				fmt.Fprintf(os.Stderr, "  ! Connect %s↔%s: HTTP %d\n", fromSlug, toSlug, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
	fmt.Fprintf(os.Stderr, "  Connected %d new pair(s), %d already present\n", created, skipped)
	return nil
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

	// GitHub credential (optional). Type CLI_TOKEN → mounted as a 0400 file
	// at /secrets/agent/GH_TOKEN + env GH_TOKEN, which the in-container `gh`
	// CLI reads directly. Same idempotent/surface-failure pattern as above.
	githubCred := seeddata.ResolveGitHubCredential()
	if githubCred != nil {
		githubID, err := seedOneCredential(client, *githubCred)
		if err != nil {
			cli.PrintWarning("GitHub credential: " + err.Error())
		} else {
			githubAssigned := 0
			for slug, agentID := range agentIDs {
				resp, err := client.Post(
					fmt.Sprintf("/api/v1/agents/%s/credentials", agentID),
					map[string]string{"credential_id": githubID, "env_var_name": githubCred.EnvVarName},
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ! Assign GitHub credential to agent %s: %v\n", slug, err)
					continue
				}
				status := resp.StatusCode
				resp.Body.Close()
				if status >= 400 && status != http.StatusConflict {
					fmt.Fprintf(os.Stderr, "  ! Assign GitHub credential to agent %s: HTTP %d\n", slug, status)
					continue
				}
				githubAssigned++
			}
			fmt.Fprintf(os.Stderr, "  + Assigned %s to %d/%d agents\n", githubCred.Name, githubAssigned, len(agentIDs))
		}
	} else {
		fmt.Fprintln(os.Stderr, "  Skipping GitHub credential (set SEED_GITHUB_TOKEN)")
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
