package main

// Model-token handoff, run immediately after `crewship login --pair`.
//
// Why here and not later. Pairing authenticates the HUMAN — it mints a CLI
// token for this terminal. The agents are a separate question: they run in
// containers and read a model token from a workspace credential. Nothing about
// pairing gives them one, and the two are easy to conflate because both are
// called "token".
//
// The order is what makes this the only cheap moment:
//
//	pair  →  [token lands here]  →  browser Launch  →  autoAssignCredentials
//
// autoAssignCredentials links workspace credentials to agents at DEPLOY time.
// A token that arrives after Launch is not delivered to the agents that
// already exist — the read-time delivery query has three arms (explicit
// agent_credentials rows, slot bindings, crew links) and a bare
// workspace-scoped credential satisfies none of them. And `crewship setup`,
// which the wizard used to point people at, answers 409 "Onboarding already
// completed" once Launch has run. So a crew launched without a token could
// not be repaired by any documented route: four agents, zero credentials, and
// the first thing the user does is send them a message.
//
// Landing the token before Launch sidesteps all of it — autoAssign finds the
// credential and links it to every agent in the crew as it is created.

import (
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"golang.org/x/term"
)

// pairedCredential is the slice of a credential row this decision needs.
// Deliberately not the full API shape: everything else here would be a field
// that could change under us for no benefit.
type pairedCredential struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
}

// needsModelTokenHandoff reports whether the workspace is missing a usable
// credential for the adapter's provider.
//
// "Usable" mirrors the delivery query's own filter (status ACTIVE): a revoked
// row is delivered to nobody, so treating it as coverage would leave the user
// exactly as stuck while suppressing the prompt that fixes it. An empty status
// is treated as active — the list endpoint omits it on some shapes, and
// defaulting the other way would nag on every pair.
func needsModelTokenHandoff(provider string, creds []pairedCredential) bool {
	for _, c := range creds {
		if !strings.EqualFold(c.Provider, provider) {
			continue
		}
		if c.Status == "" || strings.EqualFold(c.Status, "ACTIVE") {
			return false
		}
	}
	return true
}

// modelTokenCredentialPayload builds the credential body for an adapter,
// matching what onboarding stores so autoAssignCredentials treats it the same.
// Returns nil for an adapter the CLI does not know — guessing an env var name
// would store the token under a name no container reads, which fails exactly
// like the bug this exists to prevent, only more quietly.
func modelTokenCredentialPayload(adapter, token string) map[string]any {
	cfg, ok := lookupAdapter(adapter)
	if !ok {
		return nil
	}
	return map[string]any{
		"name":         cfg.envVar,
		"env_var_name": cfg.envVar,
		// AI_CLI_TOKEN, never API_KEY: onboarding refuses raw provider keys
		// and the runtime picks its auth mode off this type.
		"type":     "AI_CLI_TOKEN",
		"provider": cfg.provider,
		"value":    token,
	}
}

// looksLikeRawAnthropicAPIKey catches the single most common wrong paste.
// The server rejects these too; catching it locally costs one string compare
// and lets the message explain the difference rather than echo a 400.
func looksLikeRawAnthropicAPIKey(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), "sk-ant-api")
}

// offerModelTokenHandoff runs the post-pair credential step. Every failure
// here is soft: pairing already succeeded, and the user's terminal is now
// authenticated. Refusing to exit 0 because an optional convenience step
// could not reach the credentials endpoint would turn a working pair into a
// scary one.
func offerModelTokenHandoff(serverURL, cliToken, adapterHint string) {
	adapter := strings.TrimSpace(adapterHint)
	if adapter == "" {
		// The wizard's own default, and the only adapter verified end to end.
		adapter = "CLAUDE_CODE"
	}
	cfg, ok := lookupAdapter(adapter)
	if !ok {
		return
	}

	client := cli.NewClient(serverURL, cliToken, "")

	wsID, err := firstWorkspaceID(client)
	if err != nil || wsID == "" {
		return
	}

	creds, err := listWorkspaceCredentials(client, wsID)
	if err != nil {
		return
	}
	if !needsModelTokenHandoff(cfg.provider, creds) {
		return
	}

	// Non-interactive shells (CI, `| tee`, a provisioning script) must not
	// block on a hidden-input prompt that nobody will ever type into.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}

	fmt.Println()
	fmt.Printf("Your agents still need a %s token to call their model.\n", cfg.label)
	fmt.Println("Pairing authenticated this terminal; it did not give the agents a key.")
	fmt.Println("Paste it now and the crew you launch in the browser gets it automatically.")
	fmt.Println()

	token, err := promptAPIKey(cfg.label)
	if err != nil {
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		// A deliberate skip is a supported answer, but it must not be a
		// silent one — the consequence lands minutes later, in a chat window.
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}
	if looksLikeRawAnthropicAPIKey(token) {
		fmt.Println()
		cli.PrintWarning("That looks like a raw API key (sk-ant-api…). Crewship needs the CLI token from `claude setup-token` (an sk-ant-oat… value).")
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}

	payload := modelTokenCredentialPayload(adapter, token)
	if payload == nil {
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}

	resp, err := client.Post("/api/v1/credentials?workspace_id="+wsID, payload)
	if err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not store the token: %v", err))
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not store the token: %v", err))
		printHandoffSkipped(cfg.label, cfg.envVar)
		return
	}

	cli.PrintSuccess(fmt.Sprintf("Model token stored as %s. The crew you launch will pick it up.", cfg.envVar))
}

// printHandoffSkipped names the one repair that actually works after Launch.
// It is deliberately not "run crewship setup": that answers 409 once
// onboarding is complete, and sending someone to a command that refuses is
// worse than sending them nowhere.
func printHandoffSkipped(adapterLabel, envVar string) {
	fmt.Println()
	fmt.Println("Skipped — your agents will have no model token and cannot answer yet.")
	fmt.Println("Add one before launching a crew, or afterwards with:")
	fmt.Printf("      %screwship credential create --name %s --type AI_CLI_TOKEN \\%s\n", cli.Bold, envVar, cli.Reset)
	fmt.Printf("      %s  --provider ANTHROPIC --value $(claude setup-token)%s\n", cli.Bold, cli.Reset)
	fmt.Println("    then attach it to the crew:")
	fmt.Printf("      %screwship credential assign %s --crew <crew>%s\n", cli.Bold, envVar, cli.Reset)
}

// firstWorkspaceID resolves the workspace the freshly paired token belongs to.
// A paired CLI has no workspace selected yet — `workspace use` has not been
// run — so the config cannot answer and the server must.
func firstWorkspaceID(client *cli.Client) (string, error) {
	resp, err := client.Get("/api/v1/workspaces")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var list []workspaceRef
	if err := cli.ReadJSON(resp, &list); err != nil {
		return "", err
	}
	return oldestWorkspaceID(list), nil
}

// workspaceRef is the slice of GET /api/v1/workspaces this file needs.
type workspaceRef struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// oldestWorkspaceID picks the workspace the onboarding BACKEND would pick.
//
// This is not a tie-break detail, it is the difference between the token
// landing where the crew will look for it and landing nowhere. GET
// /api/v1/workspaces sorts `created_at DESC` (workspaces.go), while every
// onboarding handler resolves the user's membership with `ORDER BY
// wm.created_at ASC LIMIT 1` (onboarding.go). Taking list[0] therefore wrote
// the freshly paired token to the NEWEST workspace for anyone who belongs to
// more than one, while the deploy path read the oldest — and
// autoAssignCredentials links workspace credentials to agents at deploy time,
// so the crew launched with no credential at all and could not be repaired
// afterwards. That is the exact failure this file's header describes.
//
// The frontend already had to solve this (oldestWorkspaceFromWire in
// components/features/onboarding/setup-agent-api.ts); this is its CLI twin,
// down to preserving server order for legacy rows that carry no created_at.
func oldestWorkspaceID(list []workspaceRef) string {
	oldest := -1
	for i, ws := range list {
		if ws.ID == "" {
			continue
		}
		if oldest < 0 {
			oldest = i
			continue
		}
		// A row without a timestamp cannot be compared, so it never displaces
		// an incumbent — server order stands, which is the old behaviour and
		// the only safe answer when there is nothing to sort on.
		if ws.CreatedAt == "" || list[oldest].CreatedAt == "" {
			continue
		}
		if ws.CreatedAt < list[oldest].CreatedAt {
			oldest = i
		}
	}
	if oldest < 0 {
		return ""
	}
	return list[oldest].ID
}

func listWorkspaceCredentials(client *cli.Client, wsID string) ([]pairedCredential, error) {
	resp, err := client.Get("/api/v1/credentials?workspace_id=" + wsID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var creds []pairedCredential
	if err := cli.ReadJSON(resp, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}
