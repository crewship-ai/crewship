package main

// `keeper ask --credential` resolved the wrong thing and lied when it failed
// (#2413 item 4).
//
// The server matched the flag against agent_credentials.env_var_name — the
// per-agent ENV-VAR SLOT — and nothing else. The command's own help documents
// credential NAMES (`--credential npm-token`), and those came back
//
//     API error (404): credential not found for name: notion-workspace
//
// about a credential `credential list` plainly shows. The message named the
// one thing that was NOT wrong, sending the operator to look for a missing row
// instead of at the missing assignment.
//
// resolveKeeperCredential is where both halves are fixed: it accepts either
// spelling, and when neither resolves it says which of the two situations it
// is, since they need different next commands.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const keeperAgentID = "agt_riley"

func keeperAgentCredsBody() []map[string]any {
	return []map[string]any{
		{
			"credential_id":   "cred_db",
			"credential_name": "prod-db-dsn",
			"env_var_name":    "PROD_DB_DSN",
			"grant_source":    "explicit",
		},
	}
}

func TestResolveKeeperCredential_AcceptsBothSpellings(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/"+keeperAgentID+"/credentials",
		clitest.JSONResponse(200, keeperAgentCredsBody()))

	for _, ref := range []string{
		"PROD_DB_DSN", // the env-var slot — the only thing that used to work
		"prod-db-dsn", // the credential name the help documents
		"cred_db",     // the id, for a caller that already has one
	} {
		got, err := resolveKeeperCredential(newAPIClient(), keeperAgentID, "riley", ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got != "cred_db" {
			t.Errorf("%s resolved to %q, want cred_db", ref, got)
		}
	}
}

func TestResolveKeeperCredential_SlotWinsOverName(t *testing.T) {
	// A workspace can spell one credential's slot the same as another
	// credential's name. The slot is the more specific answer: it is what
	// the agent was going to read, and it is what the server matched before.
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/"+keeperAgentID+"/credentials", clitest.JSONResponse(200, []map[string]any{
		{"credential_id": "cred_a", "credential_name": "shared", "env_var_name": "OTHER"},
		{"credential_id": "cred_b", "credential_name": "unrelated", "env_var_name": "shared"},
	}))

	got, err := resolveKeeperCredential(newAPIClient(), keeperAgentID, "riley", "shared")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "cred_b" {
		t.Errorf("got %q, want cred_b (the slot match)", got)
	}
}

func TestResolveKeeperCredential_ExistsButUnassignedSaysSo(t *testing.T) {
	// The reported case: the credential is real, `credential list` shows it,
	// and it simply is not reachable by this agent.
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/"+keeperAgentID+"/credentials", clitest.JSONResponse(200, []map[string]any{}))
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": "cred_notion", "name": "notion-workspace"},
	}))

	_, err := resolveKeeperCredential(newAPIClient(), keeperAgentID, "riley", "notion-workspace")
	if err == nil {
		t.Fatal("expected an error for a credential the agent cannot reach")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exists in this workspace") || !strings.Contains(msg, "not assigned to agent riley") {
		t.Errorf("the message must say the credential exists and is unassigned, got:\n%s", msg)
	}
	if !strings.Contains(msg, "crewship credential assign notion-workspace riley") {
		t.Errorf("the message must name the command that fixes it, got:\n%s", msg)
	}
	if strings.Contains(msg, "not found for name") {
		t.Errorf("the old, misleading wording is back:\n%s", msg)
	}
}

func TestResolveKeeperCredential_TrulyUnknownSaysThat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/"+keeperAgentID+"/credentials", clitest.JSONResponse(200, []map[string]any{}))
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{}))

	_, err := resolveKeeperCredential(newAPIClient(), keeperAgentID, "riley", "no-such-thing")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no credential named \"no-such-thing\"") {
		t.Errorf("unexpected message:\n%s", err.Error())
	}
}

func TestResolveKeeperCredential_UnreadableListFallsBackToTheServer(t *testing.T) {
	// A failed enrichment must not cost the caller their request: returning
	// ("", nil) tells the command to send credential_name and let the
	// server's own slot lookup answer, exactly as before.
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/"+keeperAgentID+"/credentials", clitest.JSONResponse(500, map[string]any{"error": "boom"}))

	got, err := resolveKeeperCredential(newAPIClient(), keeperAgentID, "riley", "PROD_DB_DSN")
	if err != nil {
		t.Fatalf("a broken lookup must not fail the command: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want the empty fall-through", got)
	}
}
