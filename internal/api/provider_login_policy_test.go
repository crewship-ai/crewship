package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderAccountPolicy_RolesAndLegacyRows(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user, ws, agent, ordinary := seedAgentCredEnv(t, db)
	h := NewCredentialHandler(db, quietLogger())
	router := &Router{db: db, logger: quietLogger()}
	for _, typ := range []string{CredTypeProviderLogin, CredTypeAPIKey, CredTypeAICLIToken} {
		id := "provider-" + typ
		seedCredentialEnc(t, db, ws, user, id, id, "fake")
		if _, err := db.Exec(`UPDATE credentials SET type = ?, provider = 'OPENAI', scope = 'WORKSPACE' WHERE id = ?`, typ, id); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
			t.Run(typ+"/"+role, func(t *testing.T) {
				req := plRequest(t, "GET", "/", "", user, ws, role)
				req.SetPathValue("credentialId", id)
				visible, err := visibleCredentialNames(req, db, ws)
				if err != nil {
					t.Fatal(err)
				}
				_, got := visible[id]
				admin := role == "OWNER" || role == "ADMIN"
				if got != admin {
					t.Fatalf("provider visible=%v for %s", got, role)
				}
				if _, ok := visible[ordinary]; !ok {
					t.Fatal("ordinary workspace secret disappeared")
				}
				for _, method := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
					req.Method = method
					rr := httptest.NewRecorder()
					router.providerAccountPolicy(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(rr, req)
					want := 404
					if admin {
						want = 204
					}
					if rr.Code != want {
						t.Fatalf("%s status=%d want=%d", method, rr.Code, want)
					}
				}
			})
		}
	}
	// MANAGER cannot create a provider account or promote an ordinary key to one.
	for _, typ := range []string{CredTypeProviderLogin, CredTypeAPIKey, CredTypeAICLIToken} {
		rr := httptest.NewRecorder()
		h.Create(rr, plRequest(t, "POST", "/", `{"name":"new","type":"`+typ+`","provider":"openai","value":"fake"}`, user, ws, "MANAGER"))
		if rr.Code != 403 {
			t.Fatalf("create %s: %d %s", typ, rr.Code, rr.Body.String())
		}
		req := plRequest(t, "PATCH", "/", `{"type":"`+typ+`","provider":"openai"}`, user, ws, "MANAGER")
		req.SetPathValue("credentialId", ordinary)
		rr = httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != 403 {
			t.Fatalf("convert %s: %d %s", typ, rr.Code, rr.Body.String())
		}
	}
	// Runtime delivery remains intact, but the public agent response is redacted.
	if _, err := db.Exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('provider-grant',?,?, 'OPENAI_API_KEY')`, agent, "provider-API_KEY"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		req := plRequest(t, "GET", "/", "", user, ws, role)
		pw := loadAgentPaysWith(req.Context(), db, quietLogger(), agent, "CODEX_CLI", "OPENAI")
		if pw == nil {
			t.Fatal("missing provider identity")
		}
		if role != "OWNER" && role != "ADMIN" {
			raw, _ := json.Marshal(pw)
			if string(raw) != `{"provider":"OPENAI","restricted":true}` {
				t.Fatalf("account metadata exposed: %s", raw)
			}
		} else if pw.CredentialID != "provider-API_KEY" {
			t.Fatalf("admin lost account: %+v", pw)
		}
	}
}

func TestAgentCredentialMetadataVisibility(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user, ws, agent, hidden := seedAgentCredEnv(t, db)
	if _, err := db.Exec(`UPDATE credentials SET scope = 'CREW' WHERE id = ?`, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('hidden-grant',?,?, 'HIDDEN_KEY')`, agent, hidden); err != nil {
		t.Fatal(err)
	}
	seedCredentialEnc(t, db, ws, user, "visible-key", "VISIBLE_KEY", "fake")
	seedCredentialEnc(t, db, ws, user, "collision-key", "collision-key", "fake")
	if _, err := db.Exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('collision-grant',?,'collision-key', 'HIDDEN-KEY')`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('visible-grant',?,?, 'VISIBLE_KEY')`, agent, "visible-key"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		for _, handler := range []http.HandlerFunc{newAgentHandlerForCred(t, db).ListCredentials, NewCredentialBindingHandler(db, quietLogger()).ResolveForAgent} {
			req := plRequest(t, "GET", "/", "", user, ws, role)
			req.SetPathValue("agentId", agent)
			rr := httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != 200 {
				t.Fatalf("%s: %d %s", role, rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			if !strings.Contains(body, "visible-key") {
				t.Fatalf("visible row missing: %s", body)
			}
			if strings.Contains(body, hidden) != canRole(role, "update") {
				t.Fatalf("%s hidden row policy: %s", role, body)
			}
		}
	}
}

func TestDeviceLoginStatus_Demotion(t *testing.T) {
	f := newDeviceLoginFixture(t)
	rr := f.start(t, `{"provider":"OPENAI"}`)
	if rr.Code != 201 {
		t.Fatal(rr.Body.String())
	}
	var started deviceLoginStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.db.Exec(`UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id=?`, f.wsID, f.userID); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, started.DeviceID, f.userID); got.Status != "404" {
		t.Fatalf("demoted user sees device login: %+v", got)
	}
	req := plRequest(t, "POST", "/", `{"provider":"OPENAI"}`, f.userID, f.wsID, "MANAGER")
	rr = httptest.NewRecorder()
	f.h.Start(rr, req)
	if rr.Code != 403 {
		t.Fatalf("demoted user can start login: %d", rr.Code)
	}
}

func TestProviderAccountPolicy_CapabilitiesDoNotBypass(t *testing.T) {
	rig := newRevealRig(t)
	rig.seedWorkspace(t, "provider-cap-ws", true)
	rig.seedMember(t, "provider-cap-ws", "provider-cap-user", "MEMBER", []string{CapabilityChat, CapabilityCredentialCreate, CapabilityCredentialRotate, CapabilityCredentialReveal})
	h := NewCredentialHandler(rig.db, quietLogger())
	req := plRequest(t, "POST", "/", `{"name":"provider","type":"API_KEY","provider":"OPENAI","value":"fake"}`, "provider-cap-user", "provider-cap-ws", "MEMBER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 403 {
		t.Fatalf("credential.create bypassed provider policy: %d %s", rr.Code, rr.Body.String())
	}
	// Control: the same capability continues to allow an ordinary secret.
	req = plRequest(t, "POST", "/", `{"name":"ordinary","type":"SECRET","value":"fake"}`, "provider-cap-user", "provider-cap-ws", "MEMBER")
	rr = httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("ordinary secret capability broken: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAgentCredentialVisibility_BindingsCrewAndTenant(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user, ws, agent, hidden := seedAgentCredEnv(t, db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO crews (id,workspace_id,name,slug) VALUES ('private-crew',?,'Private','private')`, ws)
	exec(`UPDATE agents SET crew_id='private-crew' WHERE id=?`, agent)
	exec(`UPDATE credentials SET scope='CREW' WHERE id=?`, hidden)
	exec(`INSERT INTO credential_crews (credential_id,crew_id) VALUES (?,'private-crew')`, hidden)
	seedCredentialEnc(t, db, ws, user, "fallback", "FALLBACK", "fake")
	exec(`INSERT INTO credential_bindings (id,workspace_id,credential_id,scope,slot) VALUES ('fallback-binding',?, 'fallback','WORKSPACE','SHADOWED')`, ws)
	exec(`INSERT INTO credential_bindings (id,workspace_id,credential_id,scope,agent_id,slot) VALUES ('private-binding',?,?,'AGENT',?,'SHADOWED')`, ws, hidden, agent)
	seedCredentialEnc(t, db, ws, user, "deleted-key", "DELETED_KEY", "fake")
	exec(`UPDATE credentials SET deleted_at=datetime('now') WHERE id='deleted-key'`)
	exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('deleted-grant',?,'deleted-key','DELETED_KEY')`, agent)
	otherWS := "other-workspace"
	exec(`INSERT INTO workspaces (id,name,slug) VALUES (?,'Other','other')`, otherWS)
	seedCredentialEnc(t, db, otherWS, user, "foreign-key", "FOREIGN_KEY", "fake")
	exec(`INSERT INTO agent_credentials (id,agent_id,credential_id,env_var_name) VALUES ('foreign-grant',?,'foreign-key','FOREIGN_KEY')`, agent)
	for _, handler := range []http.HandlerFunc{newAgentHandlerForCred(t, db).ListCredentials, NewCredentialBindingHandler(db, quietLogger()).ResolveForAgent} {
		for _, member := range []bool{false, true} {
			if member {
				exec(`INSERT OR IGNORE INTO crew_members (crew_id,user_id) VALUES ('private-crew',?)`, user)
			} else {
				exec(`DELETE FROM crew_members WHERE crew_id='private-crew' AND user_id=?`, user)
			}
			req := plRequest(t, "GET", "/", "", user, ws, "MEMBER")
			req.SetPathValue("agentId", agent)
			rr := httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status=%d %s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			for _, forbidden := range []string{"fallback", "foreign-key", "deleted-key"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("unexpected %s: %s", forbidden, body)
				}
			}
			if strings.Contains(body, hidden) != member {
				t.Fatalf("crew member=%v response=%s", member, body)
			}
		}
	}
}
