package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetiredCredentialErasesMaterialAndPreservesHistory(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCredentialEnc(t, db, ws, user, "erase-credential", "ERASE_TOKEN", "synthetic-secret")
	execOrFatal(t, db, `UPDATE credentials SET encrypted_refresh_token='refresh',oauth_client_secret_enc='client',oauth_refresh_token_enc='oauth-refresh',username='login',account_email='person@example.test',account_label='Personal account' WHERE id='erase-credential'`)
	execOrFatal(t, db, `INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret) VALUES('erase-credential','secret','field-cipher',1)`)
	execOrFatal(t, db, `INSERT INTO credential_fields(credential_id,key,value,is_secret) VALUES('erase-credential','contact','field-person@example.test',0)`)
	execOrFatal(t, db, `INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('erase-history','erase-credential',?,'READ','{"preserve":true}')`, ws)
	var live bool
	if err := db.QueryRow(`SELECT encrypted_value!='' AND encrypted_refresh_token='refresh' FROM credentials WHERE id='erase-credential' AND deleted_at IS NULL`).Scan(&live); err != nil || !live {
		t.Fatalf("healthy live material calibration: %v", err)
	}
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("credentialId", "erase-credential")
	req = withWorkspaceUser(req, user, ws, "OWNER")
	out := httptest.NewRecorder()
	h.Delete(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("delete status %d", out.Code)
	}
	var erased bool
	if err := db.QueryRow(`SELECT deleted_at IS NOT NULL AND encrypted_value='' AND COALESCE(encrypted_refresh_token,'')='' AND COALESCE(oauth_client_secret_enc,'')='' AND COALESCE(oauth_refresh_token_enc,'')='' AND COALESCE(username,'')='' AND COALESCE(account_email,'')='' AND COALESCE(account_label,'')='' FROM credentials WHERE id='erase-credential'`).Scan(&erased); err != nil {
		t.Fatal(err)
	}
	if !erased {
		t.Error("retired credential retains secret or personal material")
	}
	var fields, history int
	if err := db.QueryRow(`SELECT count(*) FROM credential_fields WHERE credential_id='erase-credential'`).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if fields != 0 {
		t.Error("retired credential retains custom field material")
	}
	if err := db.QueryRow(`SELECT count(*) FROM credential_audit WHERE id='erase-history' AND credential_id='erase-credential' AND metadata_json='{"preserve":true}'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history lost: %d %v", history, err)
	}
}

func TestIntegrationCredentialRetirementIsAuditedAndAtomic(t *testing.T) {
	for _, fault := range []string{"healthy", "audit-abort", "audit-ignore", "retire-abort", "retire-ignore", "scrub-abort", "scrub-delete-ignore", "scrub-update-ignore", "audit-after-delete", "audit-after-update", "audit-resurrect"} {
		t.Run(fault, func(t *testing.T) {
			h, user, ws, crew := covCICFixture(t)
			db := h.db
			covCfgSeedCrewServer(t, db, "erase-server", crew, "erase-server", "stdio", "")
			agent := seedAgentRow(t, db, "erase-agent", ws, crew, "Erase", "erase-agent", "AGENT")
			execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('integration-erase',?,'github oauth erase','synthetic-cipher','OAUTH2','GITHUB',?)`, ws, user)
			execOrFatal(t, db, `INSERT INTO agent_mcp_bindings(id,agent_id,mcp_server_id,mcp_server_scope,credential_id,enabled) VALUES('erase-binding',?,'erase-server','crew','integration-erase',1)`, agent)
			execOrFatal(t, db, `INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('integration-history','integration-erase',?,'READ','{"preserve":true}')`, ws)
			execOrFatal(t, db, `INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret) VALUES('integration-erase','secret','custom-cipher',1)`)
			var ready int
			if err := db.QueryRow(`SELECT count(*) FROM agent_mcp_bindings ab JOIN credentials c ON c.id=ab.credential_id WHERE ab.id='erase-binding' AND c.deleted_at IS NULL`).Scan(&ready); err != nil || ready != 1 {
				t.Fatalf("healthy binding calibration: %d %v", ready, err)
			}
			switch fault {
			case "audit-abort":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE INSERT ON credential_audit WHEN NEW.event_type='REVOKE' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`)
			case "audit-ignore":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE INSERT ON credential_audit WHEN NEW.event_type='REVOKE' BEGIN SELECT RAISE(IGNORE); END`)
			case "retire-abort":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE UPDATE OF deleted_at ON credentials BEGIN SELECT RAISE(ABORT,'synthetic retirement fault'); END`)
			case "retire-ignore":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE UPDATE OF deleted_at ON credentials BEGIN SELECT RAISE(IGNORE); END`)
			case "scrub-abort":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE DELETE ON credential_fields BEGIN SELECT RAISE(ABORT,'synthetic scrub fault'); END`)
			case "scrub-delete-ignore":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE DELETE ON credential_fields BEGIN SELECT RAISE(IGNORE); END`)
			case "scrub-update-ignore":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault BEFORE UPDATE OF encrypted_value ON credentials WHEN NEW.encrypted_value='' BEGIN SELECT RAISE(IGNORE); END`)
			case "audit-after-delete":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault AFTER INSERT ON credential_audit WHEN NEW.event_type='REVOKE' BEGIN DELETE FROM credential_audit WHERE id=NEW.id; END`)
			case "audit-after-update":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault AFTER INSERT ON credential_audit WHEN NEW.event_type='REVOKE' BEGIN UPDATE credential_audit SET metadata_json='{"tampered":true}' WHERE id=NEW.id; END`)
			case "audit-resurrect":
				execOrFatal(t, db, `CREATE TRIGGER erase_fault AFTER INSERT ON credential_audit WHEN NEW.event_type='REVOKE' BEGIN UPDATE credentials SET deleted_at=NULL WHERE id=NEW.credential_id; END`)
			}
			out := covCICDelete(h, user, ws, crew, "erase-server")
			var servers, bindings, revokes int
			var retired bool
			for _, q := range []struct {
				sql  string
				dest any
			}{
				{`SELECT count(*) FROM crew_mcp_servers WHERE id='erase-server'`, &servers},
				{`SELECT count(*) FROM agent_mcp_bindings WHERE id='erase-binding'`, &bindings},
				{`SELECT count(*) FROM credential_audit WHERE credential_id='integration-erase' AND event_type='REVOKE'`, &revokes},
				{`SELECT deleted_at IS NOT NULL FROM credentials WHERE id='integration-erase'`, &retired},
			} {
				if err := db.QueryRow(q.sql).Scan(q.dest); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "healthy" {
				if out.Code != http.StatusOK || servers != 0 || bindings != 0 || !retired || revokes != 1 {
					t.Errorf("retirement must commit with one audit: status=%d servers=%d bindings=%d retired=%v revokes=%d", out.Code, servers, bindings, retired, revokes)
				}
			} else if out.Code != http.StatusInternalServerError || servers != 1 || bindings != 1 || retired || revokes != 0 {
				t.Errorf("failed retirement must roll back entire integration deletion: status=%d servers=%d bindings=%d retired=%v revokes=%d", out.Code, servers, bindings, retired, revokes)
			}
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("native FK violation")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetiredCredentialErasesOAuthState(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "state before retirement", true: "state after retirement"}[late], func(t *testing.T) {
			h, db := newCredHandler(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			seedCredentialEnc(t, db, ws, user, "state-retired", "STATE_OLD", "synthetic")
			seedCredentialEnc(t, db, ws, user, "state-live", "STATE_LIVE", "synthetic")
			insert := func(id, credential string) {
				execOrFatal(t, db, `INSERT INTO oauth_states(state,credential_id,workspace_id,redirect_uri,code_verifier) VALUES(?,?,?,'https://example.test/callback','synthetic-verifier')`, id, credential, ws)
			}
			insert("active-state", "state-live")
			if !late {
				insert("retired-state", "state-retired")
			}
			var calibration int
			if err := db.QueryRow(`SELECT count(*) FROM oauth_states WHERE state='active-state' AND credential_id='state-live' AND code_verifier='synthetic-verifier'`).Scan(&calibration); err != nil || calibration != 1 {
				t.Fatalf("healthy state calibration: %d %v", calibration, err)
			}
			req := httptest.NewRequest("DELETE", "/", nil)
			req.SetPathValue("credentialId", "state-retired")
			req = withWorkspaceUser(req, user, ws, "OWNER")
			out := httptest.NewRecorder()
			h.Delete(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("retirement: %d", out.Code)
			}
			if late {
				insert("retired-state", "state-retired")
			}
			var retired, active int
			if err := db.QueryRow(`SELECT count(*) FROM oauth_states WHERE credential_id='state-retired'`).Scan(&retired); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM oauth_states WHERE credential_id='state-live' AND state='active-state' AND code_verifier='synthetic-verifier'`).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if retired != 0 {
				t.Error("retired credential retains OAuth state or PKCE verifier")
			}
			if active != 1 {
				t.Error("active OAuth state changed")
			}
		})
	}
}

func TestRetirementScrubCannotEraseHistoricalIdentity(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCredentialEnc(t, db, ws, user, "identity-retired", "IDENTITY_TOKEN", "synthetic")
	execOrFatal(t, db, `CREATE TRIGGER erase_parent AFTER UPDATE OF encrypted_value ON credentials WHEN NEW.encrypted_value='' BEGIN DELETE FROM credentials WHERE id=NEW.id; END`)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("credentialId", "identity-retired")
	req = withWorkspaceUser(req, user, ws, "OWNER")
	out := httptest.NewRecorder()
	h.Delete(out, req)
	var retained bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM credentials WHERE id='identity-retired' AND deleted_at IS NULL AND encrypted_value!='')`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusInternalServerError || !retained {
		t.Errorf("scrub erased historical parent instead of rolling back: status=%d parent_retained=%v", out.Code, retained)
	}
}
