package api

// #2771: the ownership rule crew delete applies to auto-managed service
// credentials, case by case. cmd/crewship/acceptance_crew_delete_auto_
// credentials_test.go drives the reported flow through the CLI; this file
// pins every row the rule must leave alone, including the ones the CLI cannot
// produce (a second workspace, pre-v98 rows, slot bindings).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// seedCred inserts a credential row directly: several shapes below (an
// AUTO_MANAGED row attributed to a user, another workspace's row) are ones the
// create handler refuses or cannot reach from this workspace.
func seedCred(t *testing.T, db *sql.DB, id, wsID, userID, name, provider, actorType string, tag any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
		created_by, created_by_actor_type, provisioned_for_service)
		VALUES (?, ?, ?, 'enc', 'GENERIC_SECRET', ?, 'WORKSPACE', ?, ?, ?)`,
		id, wsID, name, provider, userID, actorType, tag); err != nil {
		t.Fatalf("seed credential %s: %v", id, err)
	}
}

func credDeletedAt(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var d sql.NullString
	if err := db.QueryRow(`SELECT deleted_at FROM credentials WHERE id = ?`, id).Scan(&d); err != nil {
		t.Fatalf("read credential %s: %v", id, err)
	}
	return d.String
}

func TestCrewDelete_AutoManagedCredentialOwnership(t *testing.T) {
	type fixture struct {
		db                          *sql.DB
		userID, wsID, otherWS, crew string
	}
	cases := []struct {
		name        string
		seed        func(t *testing.T, f fixture)
		wantRemoved bool
	}{
		{
			name: "minted for this crew's service, bound only to its own agent",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache/redis")
				mustExecT(t, f.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac1', 'agent-cache', 'cred', 'REDIS_PASSWORD')`)
				mustExecT(t, f.db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, slot) VALUES ('cb1', ?, 'cred', 'CREW', ?, 'REDIS_PASSWORD')`, f.wsID, f.crew)
			},
			wantRemoved: true,
		},
		{
			name: "user credential with the same name",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "NONE", "user", nil)
			},
		},
		{
			name: "AUTO_MANAGED tag without system attribution",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "user", "cache/redis")
			},
		},
		{
			name: "another crew's slug that merely starts with this one",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache-two/redis")
			},
		},
		{
			name: "same tag in another workspace",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.otherWS, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache/redis")
			},
		},
		{
			name: "bound to a live agent of another crew",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache/redis")
				mustExecT(t, f.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac1', 'agent-other', 'cred', 'REDIS_PASSWORD')`)
			},
		},
		{
			name: "bound workspace-wide",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache/redis")
				mustExecT(t, f.db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, slot) VALUES ('cb1', ?, 'cred', 'WORKSPACE', 'REDIS_PASSWORD')`, f.wsID)
			},
		},
		{
			name: "bound to another live crew",
			seed: func(t *testing.T, f fixture) {
				seedCred(t, f.db, "cred", f.wsID, f.userID, "REDIS_PASSWORD", "AUTO_MANAGED", "system", "cache/redis")
				mustExecT(t, f.db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred', 'crew-other')`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			mustExecT(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`)
			seedCrewRow(t, db, "crew-cache", wsID, "Cache", "cache")
			seedAgentRow(t, db, "agent-cache", wsID, "crew-cache", "Lead", "cache-lead", "LEAD")
			seedCrewRow(t, db, "crew-other", wsID, "Other", "other")
			seedAgentRow(t, db, "agent-other", wsID, "crew-other", "Other", "other-lead", "LEAD")
			seedCrewRow(t, db, "crew-cache-ws2", "ws-other", "Cache", "cache")

			tc.seed(t, fixture{db: db, userID: userID, wsID: wsID, otherWS: "ws-other", crew: "crew-cache"})

			h := NewCrewHandler(db, newTestLogger())
			rr := httptest.NewRecorder()
			h.Delete(rr, crewDeleteReq(userID, wsID, "crew-cache"))
			if rr.Code != http.StatusOK {
				t.Fatalf("crew delete: status = %d, body: %s", rr.Code, rr.Body.String())
			}
			var body struct {
				RemovedCredentials []string `json:"removed_credentials"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.RemovedCredentials == nil {
				t.Errorf("removed_credentials must be an array, never null: %s", rr.Body.String())
			}

			removed := credDeletedAt(t, db, "cred") != ""
			if removed != tc.wantRemoved {
				t.Fatalf("credential removed = %v, want %v (response %s)", removed, tc.wantRemoved, rr.Body.String())
			}
			if tc.wantRemoved {
				if len(body.RemovedCredentials) != 1 || body.RemovedCredentials[0] != "REDIS_PASSWORD" {
					t.Errorf("removed_credentials = %v, want [REDIS_PASSWORD]", body.RemovedCredentials)
				}
				for _, q := range []string{
					`SELECT COUNT(*) FROM agent_credentials WHERE credential_id = 'cred'`,
					`SELECT COUNT(*) FROM credential_bindings WHERE credential_id = 'cred'`,
				} {
					var n int
					if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
						t.Errorf("%s = %d (err %v); a removed credential must not keep its join rows", q, n, err)
					}
				}
			} else if len(body.RemovedCredentials) != 0 {
				t.Errorf("removed_credentials = %v, want none", body.RemovedCredentials)
			}
		})
	}
}

// Every table with a foreign key to credentials(id) is either a consumer the
// cleanup checks, or the credential's own history/data. A new consumer table
// that nobody classifies would let crew delete remove a credential something
// still uses, so an unclassified one fails here.
func TestAutoCredentialConsumers_ClassifyEveryCredentialReference(t *testing.T) {
	consumers := map[string]bool{
		"agent_credentials": true, "agent_mcp_bindings": true, "credential_crews": true,
		"credential_bindings": true, "keeper_governance_settings": true, "keeper_aux_settings": true,
		"mission_code_links": true, "provider_login_pool_members": true,
		"restricted_workflow_provider_policies": true,
	}
	history := map[string]bool{
		"credential_audit": true, "credential_rotations": true, "keeper_requests": true,
		"credential_fields": true, "provider_login_refresh": true,
		"provider_login_availability": true, "codex_login_proofs": true,
	}

	db := setupTestDB(t)
	rows, err := db.Query(`SELECT m.name FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) p
		WHERE m.type = 'table' AND p."table" = 'credentials'`)
	if err != nil {
		t.Fatalf("list foreign keys: %v", err)
	}
	defer rows.Close()
	var unclassified []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		if !consumers[table] && !history[table] {
			unclassified = append(unclassified, table)
		}
		if consumers[table] && !strings.Contains(autoCredentialConsumers, " "+table+" ") {
			t.Errorf("%s is classified as a consumer but autoCredentialConsumers never checks it", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("tables referencing credentials(id) that crew delete's auto-credential cleanup does not "+
			"classify: %v. If a row there means something still uses the credential, add it to "+
			"autoCredentialConsumers (crew_delete_auto_credentials.go); otherwise list it as history here.", unclassified)
	}
}

func mustExecT(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
