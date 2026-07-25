package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"
)

// Lease enforcement at the DELIVERY paths (#1373).
//
// The first increment gated the lease at /keeper/execute only. Three other
// resolvers read agent_credentials and hand the DECRYPTED value to an agent with
// no expiry filter at all:
//
//	InternalHandler.resolveAgentCredentials  — the BOOT path: env vars,
//	                                           /secrets files, sidecar credstore
//	QueryHandler.loadAgentCredentials        — the peer-query path
//	AssignmentHandler.loadAgentCredentials   — the delegation/hire path
//
// A lapsed lease delivered through any of them is a standing credential wearing
// a lease's label, so each gets the same fail-closed gate.

// seedLeasedGrant seeds a fresh user+workspace and then the two grants.
func seedLeasedGrant(t *testing.T, db *sql.DB, prefix string) (agentID, standingEnv, expiredEnv string) {
	t.Helper()
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return seedLeasedGrantIn(t, db, prefix, userID, wsID)
}

// seedLeasedGrantIn seeds one agent with two credentials in an EXISTING
// workspace: one on a standing grant and one whose lease lapsed a minute ago.
// Returns (agentID, standingEnvVar, expiredEnvVar). Split from seedLeasedGrant
// so the QueryHandler tests can reuse the workspace newQueryHandler already
// created instead of tripping the users.email UNIQUE constraint.
func seedLeasedGrantIn(t *testing.T, db *sql.DB, prefix, userID, wsID string) (agentID, standingEnv, expiredEnv string) {
	t.Helper()
	ensureEncryptionKey(t)
	crewID := prefix + "-crew"
	agentID = prefix + "-ag"
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', ?)`,
		crewID, wsID, prefix+"-c")
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', ?)`,
		agentID, crewID, wsID, prefix+"-a")

	standingEnv, expiredEnv = "TOK_STANDING", "TOK_LEASED"
	seedCredentialEnc(t, db, wsID, userID, prefix+"-standing", prefix+"-standing", "standing-token")
	seedCredentialEnc(t, db, wsID, userID, prefix+"-leased", prefix+"-leased", "leased-token")

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES (?, ?, ?, ?, 0, datetime('now'))`,
		prefix+"-ac1", agentID, prefix+"-standing", standingEnv)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at, expires_at, lease_source)
		VALUES (?, ?, ?, ?, 1, datetime('now'), ?, ?)`,
		prefix+"-ac2", agentID, prefix+"-leased", expiredEnv, past, leaseSourceKeeperAllow)
	return agentID, standingEnv, expiredEnv
}

// TestLease_BootResolver_RefusesExpiredLease is the highest-stakes of the three:
// resolveAgentCredentials feeds the agent's boot payload, so an expired lease
// slipping through here means the plaintext lands in the container's env, in a
// /secrets file, and in the sidecar credstore for the container's whole life.
func TestLease_BootResolver_RefusesExpiredLease(t *testing.T) {
	db := setupTestDB(t)
	h := covCfgHandler(db)
	agentID, standingEnv, expiredEnv := seedLeasedGrant(t, db, "boot")

	creds, err := h.resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentID)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}
	for _, c := range creds {
		if c.EnvVar == expiredEnv {
			t.Fatalf("expired lease delivered at boot as %s — the whole container lifetime holds it", c.EnvVar)
		}
	}
	if len(creds) != 1 || creds[0].EnvVar != standingEnv {
		t.Fatalf("creds = %+v, want only the standing grant %s", creds, standingEnv)
	}
	if creds[0].Value != "standing-token" {
		t.Errorf("standing grant value = %q, want the decrypted token", creds[0].Value)
	}
}

// TestLease_PeerQueryResolver_RefusesExpiredLease covers the peer-query loader.
func TestLease_PeerQueryResolver_RefusesExpiredLease(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)
	agentID, standingEnv, expiredEnv := seedLeasedGrantIn(t, h.db, "peer", userID, wsID)

	creds, err := h.loadAgentCredentials(context.Background(), agentID)
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	for _, c := range creds {
		if c.EnvVarName == expiredEnv {
			t.Fatalf("expired lease injected into a peer query as %s", c.EnvVarName)
		}
	}
	if len(creds) != 1 || creds[0].EnvVarName != standingEnv {
		t.Fatalf("creds = %+v, want only the standing grant %s", creds, standingEnv)
	}
}

// TestLease_DelegationResolver_RefusesExpiredLease covers the delegation/hire
// loader — the sub-agent boundary, where a lapsed lease would be handed to an
// agent that was never the one the lease was issued to.
func TestLease_DelegationResolver_RefusesExpiredLease(t *testing.T) {
	db := setupTestDB(t)
	agentID, standingEnv, expiredEnv := seedLeasedGrant(t, db, "dele")

	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	creds, err := h.loadAgentCredentials(context.Background(), agentID)
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	for _, c := range creds {
		if c.EnvVarName == expiredEnv {
			t.Fatalf("expired lease injected at the delegation boundary as %s", c.EnvVarName)
		}
	}
	if len(creds) != 1 || creds[0].EnvVarName != standingEnv {
		t.Fatalf("creds = %+v, want only the standing grant %s", creds, standingEnv)
	}
}

// TestLease_LiveLeaseStillDelivered is the availability half of the contract: a
// lease that has NOT lapsed must still be delivered by all three resolvers.
// Without this, a too-eager gate would look like a passing security fix while
// breaking every leased credential immediately.
func TestLease_LiveLeaseStillDelivered(t *testing.T) {
	db := setupTestDB(t)
	agentID, _, leasedEnv := seedLeasedGrant(t, db, "live")
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	execOrFatal(t, db, `UPDATE agent_credentials SET expires_at = ? WHERE agent_id = ? AND env_var_name = ?`,
		future, agentID, leasedEnv)

	t.Run("boot", func(t *testing.T) {
		creds, err := covCfgHandler(db).resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentID)
		if err != nil {
			t.Fatalf("resolveAgentCredentials: %v", err)
		}
		if len(creds) != 2 {
			t.Fatalf("creds = %d, want 2 (standing + live lease)", len(creds))
		}
	})
	t.Run("delegation", func(t *testing.T) {
		creds, err := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger()).
			loadAgentCredentials(context.Background(), agentID)
		if err != nil {
			t.Fatalf("loadAgentCredentials: %v", err)
		}
		if len(creds) != 2 {
			t.Fatalf("creds = %d, want 2 (standing + live lease)", len(creds))
		}
	})
}

// TestLease_PeerQueryResolver_SkipsNonActive is the #1051 gap in the third
// loader: QueryHandler.loadAgentCredentials never got the status='ACTIVE' filter
// that resolveAgentCredentials and the delegation loader received, so a PENDING
// credential's sentinel body ("pending_oauth") was decrypted and injected as a
// real env value on the peer-query path.
func TestLease_PeerQueryResolver_SkipsNonActive(t *testing.T) {
	h, userID, wsID, _, _, _ := newQueryHandler(t)
	ensureEncryptionKey(t)
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('pq-crew', ?, 'C', 'pq-c')`, wsID)
	execOrFatal(t, h.db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('pq-ag', 'pq-crew', ?, 'A', 'pq-a')`, wsID)

	seedCredentialEnc(t, h.db, wsID, userID, "pq-active", "pq-active", "real-token")
	seedCredentialEnc(t, h.db, wsID, userID, "pq-pending", "pq-pending", pendingSentinelOAuth)
	execOrFatal(t, h.db, `UPDATE credentials SET status='PENDING' WHERE id='pq-pending'`)
	execOrFatal(t, h.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('pq-ac1','pq-ag','pq-active','TOK_A',0,datetime('now'))`)
	execOrFatal(t, h.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('pq-ac2','pq-ag','pq-pending','TOK_P',1,datetime('now'))`)

	creds, err := h.loadAgentCredentials(context.Background(), "pq-ag")
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].EnvVarName != "TOK_A" {
		t.Fatalf("creds = %+v, want only the ACTIVE TOK_A", creds)
	}
	for _, c := range creds {
		if isPendingSentinel(c.PlainValue) {
			t.Fatalf("PENDING sentinel leaked into a peer query: %+v", c)
		}
	}
}
