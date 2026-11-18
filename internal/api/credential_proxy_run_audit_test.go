package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestProxyAdmissionRunAuditScopeAndFailure(t *testing.T) {
	for _, scenario := range []string{"valid", "legacy", "unknown run", "peer run", "foreign workspace", "foreign crew", "missing crew", "crew-only run", "duplicate start", "ended run", "expired grant", "revoked credential", "hard budget", "audit failure", "no credential"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := costRouter(t)
			db := r.db
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			crew := seedCrewRow(t, db, "proxy-crew", ws, "Proxy", "proxy")
			agent := seedAgentRow(t, db, "proxy-agent", ws, crew, "A", "a", "AGENT")
			otherCrew := seedCrewRow(t, db, "proxy-other", ws, "Other", "other")
			peer := seedAgentRow(t, db, "proxy-peer", ws, crew, "B", "b", "AGENT")
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('proxy-key',?,'proxy-key','synthetic-should-never-appear','API_KEY','OPENAI','ACTIVE','CREW',?)`, ws, user)
			exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('proxy-grant',?,'proxy-key','OPENAI_API_KEY')`, agent)
			startAgent, startWS, startCrew := agent, ws, crew
			if scenario == "peer run" {
				startAgent = peer
			}
			if scenario == "foreign workspace" {
				startWS = "proxy-other-workspace"
				exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Other','proxy-other-workspace')`, startWS)
			}
			if scenario == "foreign crew" {
				startCrew = otherCrew
			}
			put := func(id, kind string) {
				exec(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES(?,?,?,?,?,'orchestrator','synthetic run','{}','proxy-run')`, id, startWS, startCrew, startAgent, kind)
			}
			if scenario != "unknown run" {
				put("proxy-start", "run.started")
			}
			if scenario == "duplicate start" {
				put("proxy-start-two", "run.started")
			}
			if scenario == "missing crew" {
				exec(`UPDATE journal_entries SET crew_id=NULL WHERE id='proxy-start'`)
			}
			if scenario == "ended run" {
				put("proxy-ended", "run.completed")
			}
			if scenario == "expired grant" {
				exec(`UPDATE agent_credentials SET expires_at='2000-01-01T00:00:00Z' WHERE id='proxy-grant'`)
			}
			if scenario == "revoked credential" {
				exec(`UPDATE credentials SET status='REVOKED' WHERE id='proxy-key'`)
			}
			if scenario == "hard budget" {
				exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('proxy-cap',?,'workspace',?,'day',1,'hard')`, ws, ws)
			}
			if scenario == "audit failure" {
				exec(`CREATE TRIGGER proxy_audit_failure BEFORE INSERT ON credential_audit BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
			}
			body := map[string]string{"agent_id": agent, "credential_id": "proxy-key", "provider": "OPENAI", "run_id": "proxy-run"}
			if scenario == "legacy" {
				delete(body, "run_id")
			}
			if scenario == "no credential" {
				body["credential_id"] = ""
			}
			if scenario == "crew-only run" {
				delete(body, "agent_id")
				delete(body, "credential_id")
				body["crew_id"] = crew
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(string(raw)))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(bindTestMaster, ws, crew))
			rr := httptest.NewRecorder()
			h := NewInternalHandler(db, bindTestMaster, testLogger())
			h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, req)
			want := http.StatusForbidden
			if scenario == "valid" || scenario == "legacy" || scenario == "no credential" {
				want = http.StatusOK
			}
			if scenario == "audit failure" {
				want = http.StatusServiceUnavailable
			}
			if rr.Code != want {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, want, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE event_type='PROXY_ADMITTED'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if scenario == "valid" {
				expected = 1
			}
			if count != expected {
				t.Fatalf("audit count=%d want=%d", count, expected)
			}
			var used *string
			if err := db.QueryRow(`SELECT last_used_at FROM credentials WHERE id='proxy-key'`).Scan(&used); err != nil {
				t.Fatal(err)
			}
			if used != nil {
				t.Fatal("proxy admission was incorrectly counted as legacy USE")
			}
			if count == 1 {
				var actor, metadata string
				if err := db.QueryRow(`SELECT agent_id,metadata_json FROM credential_audit WHERE event_type='PROXY_ADMITTED'`).Scan(&actor, &metadata); err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if json.Unmarshal([]byte(metadata), &m) != nil {
					t.Fatal("invalid audit metadata")
				}
				if actor != agent || m["run_id"] != "proxy-run" || m["channel"] != "proxy" || m["stage"] != "admitted" || m["authentication"] != "unknown" {
					t.Fatal("wrong audit attribution/stage")
				}
				if strings.Contains(metadata, "synthetic-should-never-appear") {
					t.Fatal("credential material reached audit")
				}
			}
		})
	}
}
