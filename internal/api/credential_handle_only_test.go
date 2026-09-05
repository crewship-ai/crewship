package api

// handle_only (#2376): the agent may USE the credential, never read it — and
// that holds on every delivery path, with Keeper on or off. Three loaders
// turn a delivered row into something an agent process receives; each is
// checked here against the same two rows so the property cannot quietly
// hold on one path and not another (the shape #2261 found for SECRET).

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// seedHandleOnlyPair grants agentID two ACTIVE CLI_TOKEN credentials — a type
// the delivery policy hands over at boot, so only handle_only stands between
// the agent and the value: one handle-only, one ordinary.
func seedHandleOnlyPair(t *testing.T, db *sql.DB, wsID, userID, agentID string) (hidden, plain string) {
	t.Helper()
	setTestEncryptionKey(t)
	for _, c := range []struct {
		id, name, value string
		handleOnly      int
	}{
		{"cred-hidden", "HIDDEN_TOKEN", "hidden-plaintext-4d5e6f", 1},
		{"cred-plain", "PLAIN_TOKEN", "plain-plaintext-1a2b3c", 0},
	} {
		enc, err := encryption.Encrypt(c.value)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
			security_level, status, created_by, handle_only)
			VALUES (?, ?, ?, ?, 'CLI_TOKEN', 'NONE', 'WORKSPACE', 1, 'ACTIVE', ?, ?)`,
			c.id, wsID, c.name, enc, userID, c.handleOnly)
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES (?, ?, ?, ?, 0, datetime('now'))`, "grant-"+c.id, agentID, c.id, c.name)
	}
	return "hidden-plaintext-4d5e6f", "plain-plaintext-1a2b3c"
}

func TestLoadDeliveredCredentials_CarriesHandleOnly(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "ho-crew", wsID, "Crew", "ho-crew")
	agentID := seedAgentRow(t, db, "ho-agent", wsID, crewID, "Agent", "ho-agent", "AGENT")
	seedHandleOnlyPair(t, db, wsID, userID, agentID)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentID)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	got := map[string]bool{}
	for _, d := range delivered {
		got[d.EnvVar] = d.HandleOnly
	}
	if !got["HIDDEN_TOKEN"] || got["PLAIN_TOKEN"] {
		t.Errorf("handle_only flags = %v, want HIDDEN_TOKEN=true PLAIN_TOKEN=false", got)
	}
}

// The three loaders, Keeper OFF — the configuration under which a SECRET would
// have been delivered in plaintext. handle_only does not consult Keeper.
func TestHandleOnly_WithheldOnEveryLoader_KeeperOff(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "ho-crew", wsID, "Crew", "ho-crew")
	agentID := seedAgentRow(t, db, "ho-agent", wsID, crewID, "Agent", "ho-agent", "AGENT")
	hidden, plain := seedHandleOnlyPair(t, db, wsID, userID, agentID)
	logger := newTestLogger()

	t.Run("peer query loader", func(t *testing.T) {
		h := NewQueryHandler(db, nil, nil, "", logger)
		creds, err := h.loadAgentCredentials(context.Background(), agentID)
		if err != nil {
			t.Fatalf("loadAgentCredentials: %v", err)
		}
		assertHandleOnlyDelivery(t, hidden, plain, func(yield func(env, value string, handleOnly bool)) {
			for _, c := range creds {
				yield(c.EnvVarName, c.PlainValue, c.HandleOnly)
			}
		})
	})
	t.Run("sub-agent boot loader", func(t *testing.T) {
		h := NewAssignmentHandler(db, nil, nil, "", logger)
		creds, err := h.loadAgentCredentials(context.Background(), agentID)
		if err != nil {
			t.Fatalf("loadAgentCredentials: %v", err)
		}
		assertHandleOnlyDelivery(t, hidden, plain, func(yield func(env, value string, handleOnly bool)) {
			for _, c := range creds {
				yield(c.EnvVarName, c.PlainValue, c.HandleOnly)
			}
		})
	})
	t.Run("agent config resolver", func(t *testing.T) {
		h := NewInternalHandler(db, "tok", logger)
		req := httptest.NewRequest("GET", "/", nil)
		creds, err := h.resolveAgentCredentials(req, agentID)
		if err != nil {
			t.Fatalf("resolveAgentCredentials: %v", err)
		}
		assertHandleOnlyDelivery(t, hidden, plain, func(yield func(env, value string, handleOnly bool)) {
			for _, c := range creds {
				yield(c.EnvVar, c.Value, c.HandleOnly)
			}
		})
	})
}

// assertHandleOnlyDelivery checks the shape every loader must produce: the
// handle-only row is PRESENT (the agent must learn its name) with an EMPTY
// value, and the ordinary row is delivered as before.
func assertHandleOnlyDelivery(t *testing.T, hidden, plain string, each func(yield func(env, value string, handleOnly bool))) {
	t.Helper()
	seenHidden, seenPlain := false, false
	each(func(env, value string, handleOnly bool) {
		switch env {
		case "HIDDEN_TOKEN":
			seenHidden = true
			if value != "" {
				t.Errorf("HIDDEN_TOKEN delivered with a value (%d bytes) — handle_only must leave it empty", len(value))
			}
			if !handleOnly {
				t.Error("HIDDEN_TOKEN must be flagged handle_only on the delivered entry")
			}
		case "PLAIN_TOKEN":
			seenPlain = true
			if value != plain {
				t.Errorf("PLAIN_TOKEN value = %q, want delivered unchanged", value)
			}
		}
		if strings.Contains(value, hidden) {
			t.Errorf("the handle-only value reached the delivered set under %s", env)
		}
	})
	if !seenHidden {
		t.Error("the handle-only credential must still be delivered BY NAME so the agent can use it via /keeper/execute")
	}
	if !seenPlain {
		t.Error("the ordinary credential went missing")
	}
}

// The [KEEPER] prompt block is where /keeper/execute is taught, so a
// handle-only credential is listed there whatever its type.
func TestBuildKeeperBlock_ListsHandleOnly(t *testing.T) {
	h := &InternalHandler{logger: newTestLogger()}
	block := h.buildKeeperBlock("ho-agent", []mcpCredEntry{
		{EnvVar: "PG_PASSWORD", Type: "CLI_TOKEN", HandleOnly: true},
		{EnvVar: "PLAIN_TOKEN", Type: "CLI_TOKEN"},
	})
	if !strings.Contains(block, "- PG_PASSWORD") {
		t.Errorf("handle-only credential missing from the Keeper block:\n%s", block)
	}
	if strings.Contains(block, "PLAIN_TOKEN") {
		t.Errorf("an ordinary boot-delivered credential must not be listed as Keeper-guarded:\n%s", block)
	}
}

// /keeper/execute is the way a handle-only credential is USED, so its lookup
// must find the row: ACTIVE, granted, handle_only — the shape supply leaves.
func TestKeeperExecuteLookup_FindsHandleOnlyGrant(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "ho-crew", wsID, "Crew", "ho-crew")
	agentID := seedAgentRow(t, db, "ho-agent", wsID, crewID, "Agent", "ho-agent", "AGENT")
	seedHandleOnlyPair(t, db, wsID, userID, agentID)

	// The same predicate HandleExecute runs before injecting (keeper_execute.go).
	var name string
	err := db.QueryRow(`
		SELECT c.name FROM credentials c
		JOIN agent_credentials ac ON ac.credential_id = c.id
		WHERE ac.agent_id = ? AND ac.env_var_name = ? AND c.workspace_id = ?
		  AND c.status = 'ACTIVE' AND c.deleted_at IS NULL
		  AND (ac.expires_at IS NULL OR ac.expires_at > ?)`,
		agentID, "HIDDEN_TOKEN", wsID, leaseComparisonNow()).Scan(&name)
	if err != nil {
		t.Fatalf("keeper execute lookup must resolve a handle-only grant by name: %v", err)
	}
}
