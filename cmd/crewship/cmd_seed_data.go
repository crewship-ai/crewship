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

	// ActiveCrews applies the env gate (opt-in demo crews like local-Ollama are
	// excluded unless CREWSHIP_SEED_OLLAMA=1), so the default seed is unchanged.
	for _, c := range seeddata.ActiveCrews() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body := map[string]interface{}{
			"name":  c.Name,
			"slug":  c.Slug,
			"color": c.Color,
			"icon":  c.Icon,
		}
		if c.AllowPrivateEndpoints {
			body["allow_private_endpoints"] = true
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
		if len(c.AllowedDomains) > 0 {
			body["allowed_domains"] = c.AllowedDomains
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

	for _, a := range seeddata.ActiveAgents() {
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
		for k, v := range agentUpdateOnlyFields(a) {
			body[k] = v
		}
		id, err := createOrResolve(client, "/api/v1/agents", body, "/api/v1/agents", a.Slug)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", a.Slug, err)
		}
		if err := applyAgentUpdateOnlyFields(client, id, a); err != nil {
			return nil, fmt.Errorf("agent %s: %w", a.Slug, err)
		}
		ids[a.Slug] = id
		fmt.Fprintf(os.Stderr, "  + Agent: %s (%s, %s)\n", a.Name, a.AgentRole, a.ToolProfile)
	}
	return ids, nil
}

// agentUpdateOnlyFields carries the agent columns that PATCH
// /api/v1/agents/{id} accepts but POST /api/v1/agents does NOT.
//
// createAgentRequest (internal/api/agents_create.go) models a fixed set of
// columns and readJSON ignores everything else, so a key only the update
// handler knows about is accepted with a 201 and silently dropped.
// suggested_prompts and ask_forms are both such keys: they reached the update
// allow-list (internal/api/agents_update.go) and never the create struct.
//
// The manifest importer hit this first and settled the pattern — see
// buildAgentPostCreateBody in internal/manifest/plan.go, which is the same
// function for the same reason. The seeder cannot reuse it directly (it takes
// a manifest.Agent), so it restates the set here; both are pinned by tests
// that assert on the resulting RECORD rather than on the request that was
// sent, which is the only way this class of bug shows up.
//
// This is the hook for the next column of the same shape: add the field here
// and both the create body and the follow-up PATCH cover it at once.
func agentUpdateOnlyFields(a seeddata.AgentDef) map[string]interface{} {
	out := map[string]interface{}{}
	if strings.TrimSpace(a.SuggestedPrompts) != "" {
		out["suggested_prompts"] = a.SuggestedPrompts
	}
	if forms := seeddata.AgentAskForms(a.AskFormsSlug); strings.TrimSpace(forms) != "" {
		out["ask_forms"] = forms
	}
	return out
}

// applyAgentUpdateOnlyFields is the follow-up PATCH that actually persists
// them. It runs on both paths on purpose: after a fresh create, where the
// create dropped them, and after a 409 resolve, where the agent predates this
// data and a re-seed is how an existing workspace picks it up.
//
// A refusal is fatal. The server validates both columns before writing
// (normalizeSuggestedPrompts, internal/askforms), so an error here means the
// seeded definition is invalid — and an agent silently created without the
// questionnaire it was supposed to demonstrate is exactly the outcome this
// whole path exists to prevent.
func applyAgentUpdateOnlyFields(client *cli.Client, agentID string, a seeddata.AgentDef) error {
	patch := agentUpdateOnlyFields(a)
	if len(patch) == 0 {
		return nil
	}
	resp, err := client.Patch("/api/v1/agents/"+agentID, patch)
	if err != nil {
		return fmt.Errorf("set suggested prompts / ask forms: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("set suggested prompts / ask forms: %w", err)
	}
	resp.Body.Close()
	return nil
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

	// Local Ollama endpoint (optional, CREWSHIP_SEED_OLLAMA=1). Workspace-scoped
	// ENDPOINT_URL — resolved as the workspace default for OpenCode agents on an
	// ollama/* model, so no per-agent assignment is needed. Inert until the
	// operator also sets CREWSHIP_ALLOW_PRIVATE_ENDPOINTS=1 (host.docker.internal
	// is a private address blocked by the default-on SSRF fence).
	if ollamaCred := seeddata.ResolveOllamaEndpointCredential(); ollamaCred != nil {
		if _, err := seedOneCredential(client, *ollamaCred); err != nil {
			cli.PrintWarning("Ollama endpoint credential: " + err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "  + Local-model endpoint %s → %s (needs CREWSHIP_ALLOW_PRIVATE_ENDPOINTS=1)\n", ollamaCred.Name, ollamaCred.Value)
		}
	} else {
		fmt.Fprintln(os.Stderr, "  Skipping Ollama endpoint (set CREWSHIP_SEED_OLLAMA=1)")
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

// ════════════════════════════════════════════════════════════════════════════
// Demo vault — one of every credential shape
// ════════════════════════════════════════════════════════════════════════════

// seedDemoCredentials fills the vault with one credential of every shape so a
// fresh workspace shows what this surface does. Without them the page is a
// single Anthropic key: the type filter has one option, the classification
// badges never appear, the readiness column has nothing to be missing, and the
// multi-account model is a claim in a document.
//
// Every value is inert and reads as such. Failures are reported and skipped
// rather than aborting the seed — demo data is not worth failing a workspace
// over, and a half-filled vault is still a better demo than an empty one.
func seedDemoCredentials(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Seeding demo credentials (inert values)...")

	ids := map[string]string{}
	for _, dc := range seeddata.DemoCredentials() {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := seedOneDemoCredential(client, dc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", dc.Def.Name, err)
			continue
		}
		ids[dc.Def.Name] = id

		for _, f := range dc.Fields {
			body := map[string]any{"key": f.Key, "value": f.Value, "is_secret": f.Secret}
			resp, err := client.Post("/api/v1/credentials/"+id+"/fields", body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s field %s: %v\n", dc.Def.Name, f.Key, err)
				continue
			}
			// 409 means a previous run already added it — idempotent, like
			// every other seed step.
			if resp.StatusCode != http.StatusConflict {
				if err := cli.CheckError(resp); err != nil {
					fmt.Fprintf(os.Stderr, "  ! %s field %s: %v\n", dc.Def.Name, f.Key, err)
				}
			}
			resp.Body.Close()
		}

		if dc.Sensitivity != "" {
			resp, err := client.Put("/api/v1/credentials/"+id+"/sensitivity",
				map[string]string{"sensitivity": dc.Sensitivity})
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s sensitivity: %v\n", dc.Def.Name, err)
			} else {
				if err := cli.CheckError(resp); err != nil {
					fmt.Fprintf(os.Stderr, "  ! %s sensitivity: %v\n", dc.Def.Name, err)
				}
				resp.Body.Close()
			}
		}
	}

	// Bindings last: they need the credentials AND the crews to exist, and a
	// binding to a crew that failed to seed is worse than no binding.
	for _, b := range seeddata.DemoBindings() {
		credID, ok := ids[b.CredentialName]
		if !ok {
			continue
		}
		crewID, ok := crewIDs[b.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  = Binding skipped: crew %s not seeded\n", b.CrewSlug)
			continue
		}
		// A demo pack bound a REAL token to this crew's slot. The inert
		// account must not take it — binding is first-wins with a 409 for
		// the second, so this is a courtesy skip with a reason rather than a
		// swallowed conflict.
		if packBoundSlots[b.CrewSlug+"/"+b.Slot] {
			fmt.Fprintf(os.Stderr, "  = Binding skipped: %s on crew %s already carries a demo pack credential\n", b.Slot, b.CrewSlug)
			continue
		}
		resp, err := client.Post("/api/v1/credentials/bindings", map[string]string{
			"credential_id": credID,
			"slot":          b.Slot,
			"scope":         "CREW",
			"crew_id":       crewID,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Binding %s→%s: %v\n", b.Slot, b.CredentialName, err)
			continue
		}
		if resp.StatusCode != http.StatusConflict {
			if err := cli.CheckError(resp); err != nil {
				fmt.Fprintf(os.Stderr, "  ! Binding %s→%s: %v\n", b.Slot, b.CredentialName, err)
			} else {
				fmt.Fprintf(os.Stderr, "  + Binding: %s → %s (crew %s)\n", b.Slot, b.CredentialName, b.CrewSlug)
			}
		}
		resp.Body.Close()
	}
	return nil
}

// seedOneDemoCredential creates one demo credential, carrying the fields
// seedOneCredential does not: username (USERPASS requires it) and tags.
func seedOneDemoCredential(client *cli.Client, dc seeddata.DemoCredential) (string, error) {
	existingID, err := resolveByName(client, "/api/v1/credentials", dc.Def.Name)
	if err == nil && existingID != "" {
		fmt.Fprintf(os.Stderr, "  = Credential exists: %s\n", dc.Def.Name)
		return existingID, nil
	}

	body := map[string]any{
		"name":        dc.Def.Name,
		"description": dc.Def.Description,
		"type":        dc.Def.Type,
		"provider":    dc.Def.Provider,
		"value":       dc.Def.Value,
		"scope":       "WORKSPACE",
	}
	if dc.Username != "" {
		body["username"] = dc.Username
	}
	if len(dc.Tags) > 0 {
		body["tags"] = dc.Tags
	}
	// 0 means "leave the server default" (which is 1) rather than sending a
	// level the tier table does not define — an out-of-range value is read as
	// L4 everywhere, so a zero on the wire would silently mark the row critical.
	if dc.SecurityLevel > 0 {
		body["security_level"] = dc.SecurityLevel
	}
	resp, err := client.Post("/api/v1/credentials", body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return resolveByName(client, "/api/v1/credentials", dc.Def.Name)
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
	fmt.Fprintf(os.Stderr, "  + Demo credential: %s (%s)\n", dc.Def.Name, dc.Def.Type)
	return created.ID, nil
}
