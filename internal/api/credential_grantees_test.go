package api

import (
	"context"
	"strings"
	"testing"
	"time"
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

// A peer whose grant has LAPSED is not a grantee.
//
// The delivery query gates agent_credentials on credentialLeaseGateSQL and this
// derivation has to gate the same table the same way, or one expired row
// inflates the grantee set — and a set that reaches every member of the crew
// collapses to crew-wide, which erases the scoping of the credential entirely
// and hands it to the member whose lease ran out. #1373's recurring shape: the
// gate written at one resolver and missing at the next.
func TestCredentialGrantees_LapsedPeerGrantIsNotAGrantee(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "grl-crew"
		agentA = "grl-agent-a"
		agentB = "grl-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grl-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grl-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'grl-b')`, agentB, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "grl-cred", "grl-cred", `{"baseURL":"https://a.example/v1","apiKey":"sk-a"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'grl-cred'`)
	// A holds it standing; B held it under a lease that ran out yesterday.
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('grl-ac-a', ?, 'grl-cred', 'COMPAT_URL', 0, datetime('now'))`, agentA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at, created_at)
		VALUES ('grl-ac-b', ?, 'grl-cred', 'COMPAT_URL', 0, ?, datetime('now'))`,
		agentB, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339))

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	for _, d := range delivered {
		if d.ID != "grl-cred" {
			continue
		}
		if strings.Join(d.GrantedAgentIDs, ",") != agentA {
			t.Fatalf("granted_agent_ids = %v, want [%s]: a lapsed grant made the set look "+
				"like the whole crew, so the credential ships unscoped to the member "+
				"whose lease ran out", d.GrantedAgentIDs, agentA)
		}
		return
	}
	t.Fatal("agent A was not delivered its own credential")
}

// A member whose EXPLICIT grant has lapsed is not a grantee even when an
// AGENT-scoped binding also names it.
//
// The explicit agent_credentials row is authoritative for its (agent,
// credential) pair — the delivery query's binding arm suppresses on ANY such
// row, lapsed included, so B receives nothing. Counting B here is the arm-1 bug
// arriving through the binding table, and it amplifies the same way: the set
// then covers every member of the crew, collapses to crew-wide, and the
// credential ships unscoped to the member whose lease ran out.
func TestCredentialGrantees_LapsedGrantSuppressesAgentBinding(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "grs-crew"
		agentA = "grs-agent-a"
		agentB = "grs-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grs-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grs-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'grs-b')`, agentB, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "grs-cred", "grs-cred", `{"baseURL":"https://a.example/v1","apiKey":"sk-a"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'grs-cred'`)
	// A holds it outright.
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('grs-ac-a', ?, 'grs-cred', 'COMPAT_URL', 0, datetime('now'))`, agentA)
	// B has an AGENT binding for it AND a lapsed explicit grant. The grant wins
	// and suppresses the binding, so B is delivered nothing.
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, agent_id, created_at)
		VALUES ('grs-b1', ?, 'grs-cred', 'COMPAT_URL', 'AGENT', ?, datetime('now'))`, wsID, agentB)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at, created_at)
		VALUES ('grs-ac-b', ?, 'grs-cred', 'COMPAT_URL', 0, ?, datetime('now'))`,
		agentB, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339))

	// The premise: B really is delivered nothing. Assert it, so the test cannot
	// pass for the wrong reason if delivery ever changes.
	deliveredB, _, err := loadDeliveredCredentials(context.Background(), db, agentB)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials(B): %v", err)
	}
	for _, d := range deliveredB {
		if d.ID == "grs-cred" {
			t.Fatalf("premise broken: B was delivered the credential its lapsed grant removed")
		}
	}

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials(A): %v", err)
	}
	for _, d := range delivered {
		if d.ID != "grs-cred" {
			continue
		}
		if strings.Join(d.GrantedAgentIDs, ",") != agentA {
			t.Fatalf("granted_agent_ids = %v, want [%s]: an AGENT binding shadowed by a "+
				"lapsed explicit grant made the set look like the whole crew, so the "+
				"credential ships unscoped", d.GrantedAgentIDs, agentA)
		}
		return
	}
	t.Fatal("agent A was not delivered its own credential")
}

// A soft-deleted crew member is neither a member nor a grantee. If it counted
// as a grantee but not a member the set could exceed the crew and collapse to
// crew-wide; if it counted as a member but not a grantee a genuinely crew-wide
// grant would never collapse, and every crew with a deleted agent would gain
// agent ids — moving its config fingerprint and restarting its sidecar for no
// reason.
func TestCredentialGrantees_SoftDeletedMemberCountsForNeither(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "grd-crew"
		agentA = "grd-agent-a"
		agentB = "grd-agent-b"
		gone   = "grd-agent-gone"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grd-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grd-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'grd-b')`, agentB, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, deleted_at) VALUES (?, ?, ?, 'G', 'grd-g', datetime('now'))`,
		gone, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "grd-cred", "grd-cred", "tok")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'grd-cred'`)
	// Both LIVE members hold it; the deleted one does not. That is the whole
	// live crew, so it must collapse to crew-wide.
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('grd-ac-a', ?, 'grd-cred', 'ANTHROPIC_API_KEY', 0, datetime('now'))`, agentA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('grd-ac-b', ?, 'grd-cred', 'ANTHROPIC_API_KEY', 0, datetime('now'))`, agentB)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	for _, d := range delivered {
		if d.ID != "grd-cred" {
			continue
		}
		if d.GrantedAgentIDs != nil {
			t.Fatalf("granted_agent_ids = %v, want nil: every LIVE member holds it, so a "+
				"soft-deleted agent must not keep the set from collapsing to crew-wide",
				d.GrantedAgentIDs)
		}
		return
	}
	t.Fatal("agent A was not delivered the credential")
}

// Arm 4: a CREW-scoped binding reaches every member, so it is crew-wide and
// names nobody — the same answer the crew-link arm gives, through the binding
// table. Untested it would be indistinguishable from the arm being absent, and
// an absent crew-wide arm classifies a credential NARROWER than it is
// delivered, which is a 503 for members that legitimately hold it.
func TestCredentialGrantees_CrewScopedBindingIsCrewWide(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "grw-crew"
		agentA = "grw-agent-a"
		agentB = "grw-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'grw-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'grw-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'grw-b')`, agentB, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "grw-cred", "grw-cred", "tok")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'grw-cred'`)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, crew_id, created_at)
		VALUES ('grw-b1', ?, 'grw-cred', 'ANTHROPIC_API_KEY', 'CREW', ?, datetime('now'))`, wsID, crewID)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	for _, d := range delivered {
		if d.ID != "grw-cred" {
			continue
		}
		if d.GrantedAgentIDs != nil {
			t.Errorf("granted_agent_ids = %v, want nil: a CREW-scoped binding reaches "+
				"every member", d.GrantedAgentIDs)
		}
		return
	}
	t.Fatal("the CREW-scoped binding delivered nothing")
}
