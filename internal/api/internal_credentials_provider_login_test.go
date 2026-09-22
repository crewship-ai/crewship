package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderLoginReaperMetadataFollowsGrants(t *testing.T) {
	h, db, uid, ws := covICRig(t)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('login-crew',?,'Crew','login-crew')`, ws)
	mustExec(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('login-agent',?,'login-crew','Agent','login-agent')`, ws)
	seedLeasedAICred(t, db, ws, uid, "login-cred", "login-agent", "private-fixture", "")
	mustExec(t, db, `UPDATE credentials SET type='PROVIDER_LOGIN',provider='ZAI_CODING_PLAN',scope='WORKSPACE' WHERE id='login-cred'`)
	check := func(want bool) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/internal/credentials?workspace_id="+ws+"&crew_id=login-crew", nil)
		req.RemoteAddr = "172.17.0.5:1234"
		w := httptest.NewRecorder()
		h.ListCredentials(w, req)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r["id"] == "login-cred" {
				found = true
				if _, ok := r["access_token"]; ok {
					t.Fatal("metadata disclosed value")
				}
			}
		}
		if found != want {
			t.Fatalf("credential present=%v, want %v", found, want)
		}
	}
	check(true)
	mustExec(t, db, `DELETE FROM agent_credentials WHERE credential_id='login-cred'`)
	check(false)
	mustExec(t, db, `INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,agent_id,slot) VALUES('login-binding',?,'login-cred','AGENT','login-agent','ZAI_CODING_PLAN_API_KEY')`, ws)
	check(true)
	// An active key must not enter the loopback plaintext token pool.
	poolReq := httptest.NewRequest("GET", "/api/v1/internal/credentials?workspace_id="+ws+"&include_values=true", nil)
	poolReq.RemoteAddr = "127.0.0.1:1234"
	pool := httptest.NewRecorder()
	h.ListCredentials(pool, poolReq)
	if pool.Code != 200 || strings.Contains(pool.Body.String(), "login-cred") {
		t.Fatalf("provider login entered global pool: %d %s", pool.Code, pool.Body.String())
	}
	mustExec(t, db, `UPDATE agents SET deleted_at=datetime('now') WHERE id='login-agent'`)
	check(false)
	mustExec(t, db, `UPDATE agents SET deleted_at=NULL WHERE id='login-agent'`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('other-crew',?,'Other','other-crew')`, ws)
	mustExec(t, db, `UPDATE credential_bindings SET scope='CREW',agent_id=NULL,crew_id='other-crew' WHERE id='login-binding'`)
	check(false)
	mustExec(t, db, `UPDATE credential_bindings SET crew_id='login-crew' WHERE id='login-binding'`)
	check(true)
	mustExec(t, db, `UPDATE credential_bindings SET scope='WORKSPACE',crew_id=NULL WHERE id='login-binding'`)
	check(true)
	mustExec(t, db, `UPDATE credentials SET status='REVOKED' WHERE id='login-cred'`)
	check(false)
}
