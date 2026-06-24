package main

// Crew → integration wiring (Slack, GitHub, Sentry, etc.) extracted
// from cmd_seed_data.go. resolveCrewIntegration is the only helper
// that crosses concerns; kept colocated with its sole caller.

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

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
			// Fill secret placeholders (e.g. GITHUB_PERSONAL_ACCESS_TOKEN from
			// SEED_GITHUB_TOKEN) so a stdio MCP server isn't seeded unauthenticated.
			body["env_json"] = seeddata.ResolveIntegrationEnvJSON(integ.EnvJSON)
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
		// Mirror the integration-create parse-failure handling above so
		// OAuth provisioning is debuggable on its own: surface ReadJSON
		// errors and skip tracking instead of silently leaving
		// oauthCredIDs without an entry for this integration.
		if err := cli.ReadJSON(resp, &created); err != nil {
			fmt.Fprintf(os.Stderr, "  ! OAuth credential %s: parse response: %v\n", oc.CredName, err)
			continue
		}
		if created.ID == "" {
			fmt.Fprintf(os.Stderr, "  ! OAuth credential %s: response missing id\n", oc.CredName)
			continue
		}
		oauthCredIDs[oc.IntegrationName] = created.ID
		status := "ACTIVE"
		if oc.AccessToken == "" {
			status = "PENDING"
		}
		fmt.Fprintf(os.Stderr, "  + OAuth credential: %s (%s)\n", oc.CredName, status)
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
