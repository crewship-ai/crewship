package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
// Helpers
// ════════════════════════════════════════════════════════════════════════════

// createOrResolve creates a resource via POST. On 409, resolves existing by slug.
func createOrResolve(client *cli.Client, createPath string, body interface{}, listPath, slug string) (string, error) {
	resp, err := client.Post(createPath, body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return resolveBySlug(client, listPath, slug)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct{ ID string `json:"id"` }
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
