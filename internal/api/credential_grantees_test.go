package api

import (
	"context"
	"strings"
	"testing"
)

// #2052 — the agent dimension the crew-wide sidecar CredStore needs, computed at
// the one place the delivery set is derived.
//
// Two properties are being asserted, and the second is the one that is easy to
// lose: the value must be the same for EVERY member of the crew. It feeds
// sidecarConfigFingerprint, and a value that differed per member would restart
// the crew's shared sidecar on every alternation between them.
func TestCredentialGrantees_ScopeIsCredentialWideNotPerDelivery(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID  = "gr-crew"
		agentA  = "gr-agent-a"
		agentB  = "gr-agent-b"
		agentC  = "gr-agent-c"
		soloAgt = "gr-agent-solo"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'gr-c')`, crewID, wsID)
	for _, a := range []struct{ id, slug string }{{agentA, "gr-a"}, {agentB, "gr-b"}, {agentC, "gr-c-agent"}} {
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', ?)`,
			a.id, crewID, wsID, a.slug)
	}
	// A crew-less agent: its sidecar has no peers, so nothing it holds needs
	// scoping.
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'S', 'gr-solo')`, soloAgt, wsID)

	// 1. An endpoint credential granted to A alone — the shape in #2052's title.
	seedCredentialEnc(t, db, wsID, userID, "gr-compat-a", "gr-compat-a", `{"baseURL":"https://a.example/v1","apiKey":"sk-a"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'gr-compat-a'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('gr-ac-a', ?, 'gr-compat-a', 'COMPAT_A', 0, datetime('now'))`, agentA)

	// 2. The same credential granted explicitly to two of the three members.
	//    Both A and B must see the SAME pair, in the same order.
	seedCredentialEnc(t, db, wsID, userID, "gr-shared", "gr-shared", "tok-shared")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENROUTER', type = 'API_KEY' WHERE id = 'gr-shared'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('gr-ac-sa', ?, 'gr-shared', 'OPENROUTER_API_KEY', 0, datetime('now'))`, agentA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('gr-ac-sb', ?, 'gr-shared', 'OPENROUTER_API_KEY', 0, datetime('now'))`, agentB)

	// 3. A crew link: crew-wide, and must deliver with NO agent ids so the
	//    payload — and the crew's config fingerprint — is unchanged.
	seedCredentialEnc(t, db, wsID, userID, "gr-crewlink", "CREW_LINKED", "tok-crew")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'gr-crewlink'`)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('gr-crewlink', ?)`, crewID)

	ctx := context.Background()
	scopeOf := func(t *testing.T, agentID string) map[string][]string {
		t.Helper()
		delivered, _, err := loadDeliveredCredentials(ctx, db, agentID)
		if err != nil {
			t.Fatalf("loadDeliveredCredentials(%s): %v", agentID, err)
		}
		out := map[string][]string{}
		for _, d := range delivered {
			out[d.ID] = d.GrantedAgentIDs
		}
		return out
	}

	fromA := scopeOf(t, agentA)
	fromB := scopeOf(t, agentB)

	t.Run("a grant to one member names only that member", func(t *testing.T) {
		got, ok := fromA["gr-compat-a"]
		if !ok {
			t.Fatal("A was not delivered its own endpoint credential")
		}
		if strings.Join(got, ",") != agentA {
			t.Errorf("granted_agent_ids = %v, want [%s]: the crew-wide CredStore would "+
				"serve this endpoint to any member", got, agentA)
		}
	})

	t.Run("the same set no matter which member's exec asks", func(t *testing.T) {
		a, b := fromA["gr-shared"], fromB["gr-shared"]
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Fatalf("A sees %v, B sees %v: the boot payload differs per member, so each "+
				"exec restarts the shared sidecar the other is using", a, b)
		}
		if strings.Join(a, ",") != agentA+","+agentB {
			t.Errorf("granted_agent_ids = %v, want [%s %s] sorted", a, agentA, agentB)
		}
	})

	t.Run("a crew-linked credential names nobody", func(t *testing.T) {
		if got := fromA["gr-crewlink"]; got != nil {
			t.Errorf("granted_agent_ids = %v, want nil: a crew-wide credential must "+
				"serialise exactly as it did before ownership existed", got)
		}
	})

	t.Run("a grant covering every member collapses to crew-wide", func(t *testing.T) {
		// Give C the shared credential too: all three members now hold it, so
		// it is crew-wide in effect and must stop naming anyone — this is the
		// autoAssignCredentials shape, i.e. most crews, and the case that keeps
		// existing fingerprints from moving.
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES ('gr-ac-sc', ?, 'gr-shared', 'OPENROUTER_API_KEY', 0, datetime('now'))`, agentC)
		if got := scopeOf(t, agentA)["gr-shared"]; got != nil {
			t.Errorf("granted_agent_ids = %v, want nil once every member holds it", got)
		}
	})

	t.Run("a crew-less agent scopes nothing", func(t *testing.T) {
		seedCredentialEnc(t, db, wsID, userID, "gr-solo-cred", "gr-solo-cred", "tok-solo")
		execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'gr-solo-cred'`)
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES ('gr-ac-solo', ?, 'gr-solo-cred', 'ANTHROPIC_API_KEY', 0, datetime('now'))`, soloAgt)
		if got := scopeOf(t, soloAgt)["gr-solo-cred"]; got != nil {
			t.Errorf("granted_agent_ids = %v, want nil: a crew-less agent's sidecar has "+
				"no peers to scope against", got)
		}
	})
}

// An AGENT-scoped binding is the other narrow source, and it has to be counted
// or a credential bound to one member would be classified crew-wide and served
// to the whole crew — the exact defect, arriving through the binding table
// instead of the grant table.
func TestCredentialGrantees_AgentScopedBindingIsNarrow(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "grb-crew"
		agentA = "grb-agent-a"
		agentB = "grb-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grb-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grb-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'grb-b')`, agentB, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "grb-cred", "grb-cred", `{"baseURL":"https://a.example/v1","apiKey":"sk-a"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'grb-cred'`)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, agent_id, created_at)
		VALUES ('grb-b1', ?, 'grb-cred', 'COMPAT_URL', 'AGENT', ?, datetime('now'))`, wsID, agentA)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	for _, d := range delivered {
		if d.ID != "grb-cred" {
			continue
		}
		if strings.Join(d.GrantedAgentIDs, ",") != agentA {
			t.Fatalf("granted_agent_ids = %v, want [%s]: an AGENT-scoped binding is not crew-wide",
				d.GrantedAgentIDs, agentA)
		}
		return
	}
	t.Fatal("the AGENT-scoped binding delivered nothing")
}
