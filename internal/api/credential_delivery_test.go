package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// TestDeliveredCredentials_CarryProviderColumn drives loadDeliveredCredentials
// against all THREE arms of agentDeliveredCredentialsSQL.
//
// The three arms are a compound SELECT, so their column lists must stay aligned
// position-for-position. Adding a column to one arm only does not error — it
// shifts every later value into the wrong struct field, silently, and
// database/sql reports nothing. Covering one arm would not catch that; covering
// all three is the point of this test.
func TestDeliveredCredentials_CarryProviderColumn(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID  = "prov-crew"
		agentID = "prov-agent"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'prov-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'prov-a')`,
		agentID, crewID, wsID)

	// One credential per delivery arm, each with a distinct provider so a
	// cross-arm mix-up is visible rather than coincidentally right.
	seedCredentialEnc(t, db, wsID, userID, "cred-grant", "cred-grant", "tok-grant")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENROUTER', type = 'API_KEY' WHERE id = 'cred-grant'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac-grant', ?, 'cred-grant', 'OPENROUTER_API_KEY', 0, datetime('now'))`, agentID)

	seedCredentialEnc(t, db, wsID, userID, "cred-binding", "cred-binding", "tok-binding")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'cred-binding'`)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, crew_id, created_at)
		VALUES ('cb-1', ?, 'cred-binding', 'ANTHROPIC_API_KEY', 'CREW', ?, datetime('now'))`, wsID, crewID)

	seedCredentialEnc(t, db, wsID, userID, "cred-crewlink", "CREW_LINKED_KEY", "tok-crewlink")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'cred-crewlink'`)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred-crewlink', ?)`, crewID)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentID)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}

	byID := map[string]deliveredCredential{}
	for _, d := range delivered {
		byID[d.ID] = d
	}

	arms := []struct {
		arm, credID, wantProvider, wantEnvVar, wantValue string
	}{
		{"agent_credentials grant", "cred-grant", "OPENROUTER", "OPENROUTER_API_KEY", "tok-grant"},
		{"credential_bindings", "cred-binding", "ANTHROPIC", "ANTHROPIC_API_KEY", "tok-binding"},
		{"credential_crews link", "cred-crewlink", "OPENAI_COMPAT", "CREW_LINKED_KEY", "tok-crewlink"},
	}
	for _, a := range arms {
		t.Run(a.arm, func(t *testing.T) {
			d, ok := byID[a.credID]
			if !ok {
				t.Fatalf("%s arm delivered nothing for %s", a.arm, a.credID)
			}
			if d.Provider != a.wantProvider {
				t.Errorf("Provider = %q, want %q", d.Provider, a.wantProvider)
			}
			// The neighbouring columns prove the scan did not shift: if the
			// provider column were added to only one arm, these would hold the
			// provider string or an empty value instead.
			if d.EnvVar != a.wantEnvVar {
				t.Errorf("EnvVar = %q, want %q — the SELECT lists are misaligned", d.EnvVar, a.wantEnvVar)
			}
			if d.Type != "API_KEY" {
				t.Errorf("Type = %q, want API_KEY — the SELECT lists are misaligned", d.Type)
			}
			dec, err := decryptCredential(d.EncryptedValue)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if dec != a.wantValue {
				t.Errorf("value = %q, want %q — the SELECT lists are misaligned", dec, a.wantValue)
			}
		})
	}
}

// TestDeliveredCredentials_DefaultProviderDelivers pins what the overwhelming
// majority of live rows look like: credentials.provider is TEXT NOT NULL DEFAULT
// 'NONE', so a credential created without one delivers the literal "NONE".
//
// That value must reach the orchestrator unchanged rather than being normalised
// to "", because credTypeToProvider asks llmroute about it and llmroute must be
// the only thing deciding that "NONE" routes nowhere. Normalising here would put
// a second, silent opinion about provider identity in the delivery layer.
func TestDeliveredCredentials_DefaultProviderDelivers(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('df-crew', ?, 'C', 'df-c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('df-agent', 'df-crew', ?, 'A', 'df-a')`, wsID)

	enc, err := encryption.Encrypt("tok-default")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Inserted WITHOUT a provider column, so the schema default applies.
	execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-default', ?, 'cred-default', ?, 'API_KEY', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, enc, userID)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac-default', 'df-agent', 'cred-default', 'SOME_KEY', 0, datetime('now'))`)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, "df-agent")
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered %d credentials, want 1", len(delivered))
	}
	if got := delivered[0].Provider; got != "NONE" {
		t.Errorf("Provider = %q, want the schema default %q", got, "NONE")
	}
}
