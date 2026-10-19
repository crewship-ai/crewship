package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestWorkspaceRoleAuthorizationAudit(t *testing.T) {
	f := newAuthorizationAuditFixture(t)
	rr := f.do("PATCH", "/members/"+f.member, `{"role":"MANAGER"}`)
	if rr.Code != 200 {
		t.Fatalf("valid role mutation: HTTP%d", rr.Code)
	}
	var role string
	if err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE id=?`, f.member).Scan(&role); err != nil || role != "MANAGER" {
		t.Fatalf("committed role: %q, %v", role, err)
	}
	var count int
	var raw, actor string
	if err := f.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(payload),''),COALESCE(MAX(actor_id),'') FROM journal_entries WHERE workspace_id=? AND entry_type='member.role_changed'`, f.workspace).Scan(&count, &raw, &actor); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed role mutation missing chained audit: count=%d", count)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"subject_user_id": f.target, "member_id": f.member, "action": "role_changed", "previous_role": "MEMBER", "role": "MANAGER"}
	if actor != f.owner || !reflect.DeepEqual(payload, want) {
		t.Fatalf("wrong role audit identity/state: actor=%s payload=%s", actor, raw)
	}
	if f.published.Load() != 1 {
		t.Fatalf("role notification count=%d", f.published.Load())
	}
	f.checkChain(t)
}

func TestWorkspaceRoleLegacyAuditProjectionAtomic(t *testing.T) {
	for _, fault := range []string{"none", "ABORT", "IGNORE", "after_delete", "journal_deletes_projection", "projection_undoes_role"} {
		t.Run(fault, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			switch fault {
			case "ABORT", "IGNORE":
				action := `RAISE(IGNORE)`
				if fault == "ABORT" {
					action = `RAISE(ABORT,'settings audit unavailable')`
				}
				execOrFatal(t, f.db, `CREATE TRIGGER fail_settings_audit BEFORE INSERT ON audit_logs WHEN NEW.action='member.role_change' BEGIN SELECT `+action+`; END`)
			case "after_delete":
				execOrFatal(t, f.db, `CREATE TRIGGER delete_settings_audit AFTER INSERT ON audit_logs WHEN NEW.action='member.role_change' BEGIN DELETE FROM audit_logs WHERE id=NEW.id; END`)
			case "journal_deletes_projection":
				execOrFatal(t, f.db, `CREATE TRIGGER delete_settings_projection AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.role_changed' BEGIN DELETE FROM audit_logs WHERE action='member.role_change'; END`)
			case "projection_undoes_role":
				execOrFatal(t, f.db, `CREATE TRIGGER undo_settings_role AFTER INSERT ON audit_logs WHEN NEW.action='member.role_change' BEGIN UPDATE workspace_members SET role='MEMBER' WHERE id='test-mem-audit-subject'; END`)
			}
			rr := f.do("PATCH", "/members/"+f.member, `{"role":"MANAGER"}`)
			var role string
			if err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE id=?`, f.member).Scan(&role); err != nil {
				t.Fatal(err)
			}
			var events, rows int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.role_changed'`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='member.role_change'`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if fault != "none" {
				if rr.Code != 500 || role != "MEMBER" || events != 0 || rows != 0 || f.published.Load() != 0 {
					t.Errorf("settings audit fault committed mutation: HTTP%d role=%s journal=%d settings=%d notified=%d", rr.Code, role, events, rows, f.published.Load())
				}
			} else {
				// Calibrate the real mutation and canonical event before asking
				// for the existing Settings /api/v1/audit projection.
				if rr.Code != 200 || role != "MANAGER" || events != 1 || f.published.Load() != 1 {
					t.Fatalf("valid mutation failed: HTTP%d role=%s journal=%d notified=%d", rr.Code, role, events, f.published.Load())
				}
				if rows != 1 {
					t.Fatalf("committed role mutation missing Settings audit projection: count=%d", rows)
				}
				var actor, workspace, kind, entity, raw string
				if err := f.db.QueryRow(`SELECT user_id,workspace_id,entity_type,entity_id,metadata FROM audit_logs WHERE action='member.role_change'`).Scan(&actor, &workspace, &kind, &entity, &raw); err != nil {
					t.Fatal(err)
				}
				var meta map[string]any
				if err := json.Unmarshal([]byte(raw), &meta); err != nil {
					t.Fatal(err)
				}
				if actor != f.owner || workspace != f.workspace || kind != "WorkspaceMember" || entity != f.target || !reflect.DeepEqual(meta, map[string]any{"from": "MEMBER", "to": "MANAGER", "member_id": f.member}) {
					t.Fatalf("Settings projection changed: actor=%s workspace=%s kind=%s entity=%s metadata=%s", actor, workspace, kind, entity, raw)
				}
			}
			f.checkChain(t)
		})
	}
}

func TestWorkspaceRoleAuditFailureRollsBack(t *testing.T) {
	for _, fault := range []string{"ABORT", "IGNORE", "deferred_commit", "after_event_undo"} {
		t.Run(fault, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			var fk bool
			if err := f.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || !fk {
				t.Fatalf("fixture requires foreign keys: %v %v", fk, err)
			}
			if fault == "after_event_undo" {
				execOrFatal(t, f.db, `CREATE TRIGGER undo_role_after_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.role_changed' BEGIN UPDATE workspace_members SET role='MEMBER' WHERE id='test-mem-audit-subject'; END`)
			} else if fault == "deferred_commit" {
				execOrFatal(t, f.db, `CREATE TABLE role_audit_commit_failure(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
				execOrFatal(t, f.db, `CREATE TRIGGER fail_role_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.role_changed' BEGIN INSERT INTO role_audit_commit_failure VALUES('absent-role-audit-user'); END`)
			} else {
				action := `RAISE(IGNORE)`
				if fault == "ABORT" {
					action = `RAISE(ABORT,'role audit unavailable')`
				}
				execOrFatal(t, f.db, `CREATE TRIGGER fail_role_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.role_changed' BEGIN SELECT `+action+`; END`)
			}
			rr := f.do("PATCH", "/members/"+f.member, `{"role":"MANAGER"}`)
			if rr.Code != 500 {
				t.Errorf("audit failure must refuse role mutation: HTTP%d", rr.Code)
			}
			var role string
			if err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE id=?`, f.member).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if role != "MEMBER" {
				t.Errorf("audit failure committed role=%s", role)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.role_changed'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 || f.published.Load() != 0 {
				t.Errorf("failed mutation retained/published audit: stored=%d published=%d", count, f.published.Load())
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='member.role_change'`).Scan(&count); err != nil || count != 0 {
				t.Errorf("journal failure retained Settings projection: count=%d err=%v", count, err)
			}
			f.checkChain(t)
		})
	}
}

func TestWorkspaceRoleAuditRechecksCaller(t *testing.T) {
	for _, change := range []string{"demoted", "membership_removed", "suspended", "workspace_deleted", "restricted_elsewhere"} {
		t.Run(change, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			if change == "restricted_elsewhere" {
				// MANAGER is a legal mixed-membership fixture; a Guest cannot
				// acquire OWNER/ADMIN in any workspace under the real schema.
				execOrFatal(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id=?`, f.owner)
			}
			read := false
			f.beforeBody = func() {
				read = true
				switch change {
				case "demoted":
					execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.owner)
				case "membership_removed":
					execOrFatal(t, f.db, `DELETE FROM workspace_members WHERE user_id=?`, f.owner)
				case "suspended":
					execOrFatal(t, f.db, `UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id=?`, f.owner)
				case "workspace_deleted":
					execOrFatal(t, f.db, `UPDATE workspaces SET deleted_at='2026-10-19T00:00:00Z' WHERE id=?`, f.workspace)
				case "restricted_elsewhere":
					execOrFatal(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('role-audit-other','Other','role-audit-other')`)
					execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('role-audit-guest','role-audit-other',?,'MEMBER','restricted')`, f.owner)
				}
			}
			rr := f.do("PATCH", "/members/"+f.member, `{"role":"VIEWER"}`)
			if !read || rr.Code != 403 {
				t.Fatalf("stale caller passed: body=%v HTTP%d", read, rr.Code)
			}
			var role string
			if err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE id=?`, f.member).Scan(&role); err != nil || role != "MEMBER" {
				t.Fatalf("stale caller changed role: %s %v", role, err)
			}
			if f.published.Load() != 0 {
				t.Fatal("denied role mutation published audit")
			}
			f.checkChain(t)
		})
	}
}

func TestCapabilityAuditRetainsOldAndNewSets(t *testing.T) {
	f := newAuthorizationAuditFixture(t)
	rr := f.do("PATCH", "/members/"+f.target+"/capabilities", `{"grant":["issue.create"]}`)
	if rr.Code != 200 {
		t.Fatalf("valid capability mutation: HTTP%d", rr.Code)
	}
	var response capabilitiesGetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Capabilities, []string{"chat", "issue.create", "routine.create"}) {
		t.Fatalf("capability positive calibration: %v", response.Capabilities)
	}
	var raw string
	if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.capabilities_changed'`, f.workspace).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload["previous_capabilities"], []any{"chat", "routine.create"}) || !reflect.DeepEqual(payload["capabilities"], []any{"chat", "issue.create", "routine.create"}) {
		t.Errorf("durable audit lost old/new capability sets: %s", raw)
	}
	f.checkChain(t)
	// The one serializer serves HTTP, SSE and WS. Retained audit values are
	// not a new workspace-wide capability disclosure or an admin-history API.
	wire := journal.SerializeEntry(journal.Entry{Type: journal.EntryMemberCapabilitiesChanged, Payload: payload})
	public, ok := wire["payload"].(map[string]any)
	if !ok || !reflect.DeepEqual(public, map[string]any{"subject_user_id": f.target, "action": "capabilities_changed"}) {
		t.Fatalf("capability feed payload changed: %v", wire["payload"])
	}
}

func TestCapabilityAuditDefaultSetAndFinalState(t *testing.T) {
	for _, initial := range []string{"null_default", "empty_default", "undo_after_audit"} {
		t.Run(initial, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			if initial == "null_default" {
				execOrFatal(t, f.db, `UPDATE workspace_members SET capabilities=NULL WHERE id=?`, f.member)
			} else {
				execOrFatal(t, f.db, `UPDATE workspace_members SET capabilities='[]' WHERE id=?`, f.member)
			}
			if initial == "undo_after_audit" {
				execOrFatal(t, f.db, `CREATE TRIGGER undo_capabilities AFTER INSERT ON journal_entries WHEN NEW.entry_type='member.capabilities_changed' BEGIN UPDATE workspace_members SET capabilities='[]' WHERE id='test-mem-audit-subject'; END`)
			}
			rr := f.do("PATCH", "/members/"+f.target+"/capabilities", `{"grant":["issue.create"]}`)
			if initial == "undo_after_audit" {
				var stored string
				if err := f.db.QueryRow(`SELECT capabilities FROM workspace_members WHERE id=?`, f.member).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.capabilities_changed'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if rr.Code != 500 || stored != "[]" || count != 0 || f.published.Load() != 0 {
					t.Fatalf("audit-trigger undo acknowledged: HTTP%d stored=%s count=%d notified=%d", rr.Code, stored, count, f.published.Load())
				}
			} else {
				if rr.Code != 200 {
					t.Fatalf("default set mutation: HTTP%d", rr.Code)
				}
				var raw string
				if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE entry_type='member.capabilities_changed'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var event struct {
					Previous []string `json:"previous_capabilities"`
					Current  []string `json:"capabilities"`
				}
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(event.Previous, []string{"chat"}) || !reflect.DeepEqual(event.Current, []string{"chat", "issue.create"}) {
					t.Fatalf("default-set audit mismatch: %s", raw)
				}
			}
			f.checkChain(t)
		})
	}
}
