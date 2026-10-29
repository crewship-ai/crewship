package api

import (
	"encoding/json"
	"testing"
)

const blankAuditSetup = `{"crew_name":"Own blank audit crew","agent_name":"Inert audit agent","llm_provider":"OLLAMA"}`

func TestOnboardingBlankConfiguredAudit(t *testing.T) {
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER"} {
		t.Run(role, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			execOrFatal(t, f.db, `UPDATE users SET onboarding_completed=0 WHERE id=?`, f.owner)
			execOrFatal(t, f.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, f.workspace, f.owner)
			rr := f.do("POST", "/api/v1/onboarding/setup", blankAuditSetup)
			if rr.Code != 201 {
				t.Fatalf("normal crew-create role %s denied blank Setup: HTTP%d %s", role, rr.Code, rr.Body.String())
			}
			var result struct {
				CrewID       string `json:"crew_id"`
				AgentID      string `json:"agent_id"`
				CredentialID string `json:"credential_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var member, actor, crew, raw string
			if err := f.db.QueryRow(`SELECT id FROM crew_members WHERE crew_id=? AND user_id=? AND role IS NULL`, result.CrewID, f.owner).Scan(&member); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT actor_id,crew_id,payload FROM journal_entries WHERE workspace_id=? AND entry_type='crew.member_changed'`, f.workspace).Scan(&actor, &crew, &raw); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			membership, ok := payload["membership"].(map[string]any)
			previous, hasPrevious := payload["previous_membership"]
			inherited, hasRole := membership["role"]
			if actor != f.owner || crew != result.CrewID || result.AgentID == "" || result.CredentialID != "" || payload["member_id"] != member || payload["subject_user_id"] != f.owner || payload["action"] != "added" || !hasPrevious || previous != nil || !ok || !hasRole || inherited != nil || len(payload) != 5 {
				t.Fatal("canonical identity or native inherited membership changed")
			}
			if f.published.Load() != 1 {
				t.Fatalf("postcommit notifications=%d", f.published.Load())
			}
			f.checkChain(t) // Custom key differs from the native fallback key.
		})
	}
}

func TestOnboardingBlankAuditRollback(t *testing.T) {
	for _, fault := range []string{"abort", "ignore", "member_replaced", "member_role", "cas_undo", "crew_undo", "agent_undo", "deferred_commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			execOrFatal(t, f.db, `UPDATE users SET onboarding_completed=0 WHERE id=?`, f.owner)
			var action string
			timing := "AFTER"
			switch fault {
			case "abort":
				timing = "BEFORE"
				action = `SELECT RAISE(ABORT,'synthetic onboarding audit failure');`
			case "ignore":
				timing = "BEFORE"
				action = `SELECT RAISE(IGNORE);`
			case "member_replaced":
				action = `DELETE FROM crew_members WHERE id=json_extract(NEW.payload,'$.member_id'); INSERT INTO crew_members(id,crew_id,user_id,role,created_at) VALUES('replacement-onboarding-member',NEW.crew_id,NEW.actor_id,NULL,NEW.ts);`
			case "member_role":
				action = `UPDATE crew_members SET role='VIEWER' WHERE id=json_extract(NEW.payload,'$.member_id');`
			case "cas_undo":
				action = `UPDATE users SET onboarding_completed=0 WHERE id=NEW.actor_id;`
			case "crew_undo":
				action = `UPDATE crews SET slug='changed-after-audit' WHERE id=NEW.crew_id;`
			case "agent_undo":
				action = `UPDATE agents SET name='changed-after-audit' WHERE crew_id=NEW.crew_id;`
			case "deferred_commit":
				execOrFatal(t, f.db, `CREATE TABLE onboarding_commit_fault(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
				action = `INSERT INTO onboarding_commit_fault VALUES('absent-onboarding-user');`
			}
			execOrFatal(t, f.db, `CREATE TRIGGER onboarding_blank_fault `+timing+` INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN `+action+` END`)
			rr := f.do("POST", "/api/v1/onboarding/setup", blankAuditSetup)
			var completed, crews, members, agents, events int
			if err := f.db.QueryRow(`SELECT onboarding_completed FROM users WHERE id=?`, f.owner).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			for _, q := range []struct {
				sql string
				out *int
			}{
				{`SELECT COUNT(*) FROM crews WHERE workspace_id=?`, &crews},
				{`SELECT COUNT(*) FROM crew_members cm JOIN crews c ON c.id=cm.crew_id WHERE c.workspace_id=?`, &members},
				{`SELECT COUNT(*) FROM agents WHERE workspace_id=?`, &agents},
				{`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='crew.member_changed'`, &events},
			} {
				if err := f.db.QueryRow(q.sql, f.workspace).Scan(q.out); err != nil {
					t.Fatal(err)
				}
			}
			if rr.Code != 500 || completed != 0 || crews != 0 || members != 0 || agents != 0 || events != 0 || f.published.Load() != 0 {
				t.Fatalf("fault committed effects HTTP%d completed%d crews%d members%d agents%d events%d notify%d", rr.Code, completed, crews, members, agents, events, f.published.Load())
			}
			f.checkChain(t)
		})
	}
}

func TestOnboardingBlankFreshAuthority(t *testing.T) {
	for _, state := range []string{"VIEWER", "MEMBER", "demoted", "suspended", "removed", "sql_error", "global_restricted"} {
		t.Run(state, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			execOrFatal(t, f.db, `UPDATE users SET onboarding_completed=0 WHERE id=?`, f.owner)
			want := 403
			if state == "VIEWER" || state == "MEMBER" {
				execOrFatal(t, f.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, state, f.workspace, f.owner)
			}
			f.beforeBody = func() {
				switch state {
				case "demoted":
					execOrFatal(t, f.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
				case "suspended":
					execOrFatal(t, f.db, `UPDATE users SET suspended_at='2026-10-20T00:00:00Z' WHERE id=?`, f.owner)
				case "removed":
					execOrFatal(t, f.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
				case "sql_error":
					execOrFatal(t, f.db, `ALTER TABLE workspace_members RENAME TO unavailable_onboarding_members`)
				case "global_restricted":
					execOrFatal(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
					execOrFatal(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('onboarding-other','Other owned fixture','onboarding-other')`)
					execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('onboarding-other-member','onboarding-other',?,'VIEWER','restricted')`, f.owner)
				}
			}
			if state == "removed" {
				want = 400
			}
			if state == "sql_error" {
				want = 500
			}
			rr := f.do("POST", "/api/v1/onboarding/setup", blankAuditSetup)
			var completed, crews, events int
			if err := f.db.QueryRow(`SELECT onboarding_completed FROM users WHERE id=?`, f.owner).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id=?`, f.workspace).Scan(&crews); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='crew.member_changed'`, f.workspace).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if rr.Code != want || completed != 0 || crews != 0 || events != 0 || f.published.Load() != 0 {
				t.Fatalf("fresh standing failed HTTP%d want%d completed%d crews%d events%d notify%d", rr.Code, want, completed, crews, events, f.published.Load())
			}
		})
	}
}
