package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPermissionSettingsAtomicAudit(t *testing.T) {
	for _, operation := range []string{"workspace", "crew_update", "crew_delete"} {
		for _, fault := range []string{"healthy", "audit_abort"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				method, path, body := "PATCH", "/api/v1/workspaces/"+f.workspace, `{"allow_privileged_credentials":false}`
				action, entity, id := "workspace.update", "WORKSPACE", f.workspace
				before, after := "1", "0"
				read := `SELECT CAST(allow_privileged_credentials AS TEXT) FROM workspaces WHERE id=?`
				execOrFatal(t, f.db, `UPDATE workspaces SET allow_privileged_credentials=1 WHERE id=?`, f.workspace)
				if operation != "workspace" {
					id = seedTestCrew(t, f.db, f.workspace)
					entity, action = "CREW", "crew.update"
					path, body = "/api/v1/crews/"+id, `{"name":"Changed crew"}`
					before, after, read = "Test Crew", "Changed crew", `SELECT name FROM crews WHERE id=?`
					if operation == "crew_delete" {
						method, body, action = "DELETE", "", "crew.delete"
						before, after, read = "0", "1", `SELECT CAST(deleted_at IS NOT NULL AS TEXT) FROM crews WHERE id=?`
						seedCrewMemberRow(t, f.db, "settings-delete-member", id, f.target, "")
					}
				}
				if fault == "audit_abort" {
					execOrFatal(t, f.db, `CREATE TRIGGER stop_settings_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type IN ('audit.entity_updated','audit.entity_deleted') BEGIN SELECT RAISE(ABORT,'settings audit unavailable'); END`)
				}
				rr := f.do(method, path, body)
				var saved string
				if err := f.db.QueryRow(read, id).Scan(&saved); err != nil {
					t.Fatal(err)
				}
				var count, legacy int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type IN ('audit.entity_updated','audit.entity_deleted')`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=? AND entity_id=?`, action, id).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if fault != "healthy" {
					if rr.Code != 500 || saved != before || count != 0 || legacy != 0 || f.published.Load() != 0 {
						t.Errorf("audit fault: HTTP%d state=%q want=%q chain=%d legacy=%d callbacks=%d", rr.Code, saved, before, count, legacy, f.published.Load())
					}
					if operation == "crew_delete" {
						var members int
						if err := f.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE crew_id=?`, id).Scan(&members); err != nil || members != 1 {
							t.Errorf("cascade not rolled back: members=%d err=%v", members, err)
						}
					}
					return
				}
				if rr.Code != 200 || saved != after {
					t.Fatalf("healthy calibration HTTP%d state=%q want=%q: %s", rr.Code, saved, after, rr.Body.String())
				}
				if count != 1 || legacy != 1 || f.published.Load() != 1 {
					t.Fatalf("missing atomic dual audit: chain=%d legacy=%d callbacks=%d", count, legacy, f.published.Load())
				}
				var actor, raw string
				if err := f.db.QueryRow(`SELECT actor_id,payload FROM journal_entries WHERE entry_type IN ('audit.entity_updated','audit.entity_deleted')`).Scan(&actor, &raw); err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				if actor != f.owner || payload["action"] != action || payload["entity_type"] != entity || payload["entity_id"] != id {
					t.Fatalf("wrong audit binding: actor=%s payload=%v", actor, payload)
				}
				for _, forbidden := range []string{"token", "password", "mcp_config_json", "services_json"} {
					if strings.Contains(raw, fmt.Sprintf(`"%s":`, forbidden)) {
						t.Errorf("private value in audit: %s", forbidden)
					}
				}
				f.checkChain(t)
			})
		}
	}
}

// These faults run after the calibrated HTTP boundary, against real SQLite
// triggers/commit constraints. No provider, runtime or credential is involved.
func TestPermissionSettingsFaultBoundaries(t *testing.T) {
	for _, operation := range []string{"workspace", "crew_update", "crew_delete"} {
		for _, fault := range []string{"journal_ignore", "legacy_ignore", "legacy_after_delete", "after_undo", "deferred_commit"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				method, path, body := "PATCH", "/api/v1/workspaces/"+f.workspace, `{"allow_privileged_credentials":false}`
				id, before, read := f.workspace, "1", `SELECT CAST(allow_privileged_credentials AS TEXT) FROM workspaces WHERE id=?`
				execOrFatal(t, f.db, `UPDATE workspaces SET allow_privileged_credentials=1 WHERE id=?`, id)
				undo := `UPDATE workspaces SET allow_privileged_credentials=1 WHERE id='` + id + `';`
				if operation != "workspace" {
					id = seedTestCrew(t, f.db, f.workspace)
					path, body = "/api/v1/crews/"+id, `{"name":"Changed crew"}`
					before, read = "Test Crew", `SELECT name FROM crews WHERE id=?`
					undo = `UPDATE crews SET name='Test Crew' WHERE id='` + id + `';`
					if operation == "crew_delete" {
						method, body = "DELETE", ""
						before, read = "0", `SELECT CAST(deleted_at IS NOT NULL AS TEXT) FROM crews WHERE id=?`
						undo = `UPDATE crews SET deleted_at=NULL WHERE id='` + id + `';`
						seedCrewMemberRow(t, f.db, "fault-delete-member", id, f.target, "")
					}
				}
				switch fault {
				case "journal_ignore":
					execOrFatal(t, f.db, `CREATE TRIGGER fault_settings BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(IGNORE); END`)
				case "legacy_ignore":
					execOrFatal(t, f.db, `CREATE TRIGGER fault_settings BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`)
				case "legacy_after_delete":
					execOrFatal(t, f.db, `CREATE TRIGGER fault_settings AFTER INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN DELETE FROM audit_logs WHERE entity_id='`+id+`'; END`)
				case "after_undo":
					execOrFatal(t, f.db, `CREATE TRIGGER fault_settings AFTER INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN `+undo+` END`)
				case "deferred_commit":
					execOrFatal(t, f.db, `CREATE TABLE settings_commit_fault(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
					execOrFatal(t, f.db, `CREATE TRIGGER fault_settings AFTER INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN INSERT INTO settings_commit_fault VALUES('absent-settings-user'); END`)
				}
				rr := f.do(method, path, body)
				var saved string
				if err := f.db.QueryRow(read, id).Scan(&saved); err != nil {
					t.Fatal(err)
				}
				var audits, legacy int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'audit.entity_%'`).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE entity_id=?`, id).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if rr.Code != 500 || saved != before || audits != 0 || legacy != 0 || f.published.Load() != 0 {
					t.Errorf("fault committed: HTTP%d state=%q want=%q audits=%d legacy=%d callbacks=%d", rr.Code, saved, before, audits, legacy, f.published.Load())
				}
				if operation == "crew_delete" {
					var members int
					if err := f.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE crew_id=?`, id).Scan(&members); err != nil || members != 1 {
						t.Errorf("lost crew members: %d %v", members, err)
					}
				}
				f.checkChain(t)
			})
		}
	}
}

func TestPermissionSettingsFreshActor(t *testing.T) {
	for _, operation := range []string{"workspace", "crew_update"} {
		t.Run(operation, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			path, body := "/api/v1/workspaces/"+f.workspace, `{"allow_privileged_credentials":false}`
			read, id := `SELECT CAST(allow_privileged_credentials AS TEXT) FROM workspaces WHERE id=?`, f.workspace
			before := "1"
			execOrFatal(t, f.db, `UPDATE workspaces SET allow_privileged_credentials=1 WHERE id=?`, id)
			if operation == "crew_update" {
				id = seedTestCrew(t, f.db, f.workspace)
				path, body = "/api/v1/crews/"+id, `{"name":"Changed crew"}`
				read, before = `SELECT name FROM crews WHERE id=?`, "Test Crew"
			}
			reached := false
			f.beforeBody = func() {
				reached = true
				execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.owner)
			}
			rr := f.do("PATCH", path, body)
			var saved string
			if err := f.db.QueryRow(read, id).Scan(&saved); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'audit.entity_%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if !reached || rr.Code != 403 || saved != before || count != 0 || f.published.Load() != 0 {
				t.Errorf("stale actor accepted: read=%v HTTP%d state=%q want=%q audits=%d callbacks=%d", reached, rr.Code, saved, before, count, f.published.Load())
			}
		})
	}
}

// Canonical security facts explain a privilege change without copying the
// surrounding runtime config, environment, mount paths or credential material.
func TestPermissionSettingsSecurityBeforeAfter(t *testing.T) {
	for _, kind := range []string{"workspace", "crew"} {
		t.Run(kind, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			id, action, path, body := f.workspace, "workspace.update", "/api/v1/workspaces/"+f.workspace, `{"allow_privileged_credentials":false}`
			execOrFatal(t, f.db, `UPDATE workspaces SET allow_privileged_credentials=1 WHERE id=?`, f.workspace)
			if kind == "crew" {
				id = seedTestCrew(t, f.db, f.workspace)
				action = "crew.update"
				path = "/api/v1/crews/" + id
				execOrFatal(t, f.db, `UPDATE crews SET devcontainer_config=? WHERE id=?`, `{"image":"fixture-image","privileged":true,"containerEnv":{"PRIVATE_VALUE":"never-copy-this"}}`, id)
				body = `{"devcontainer_config":"{\"image\":\"fixture-image\",\"privileged\":false,\"capAdd\":[\"NET_BIND_SERVICE\"]}"}`
			}
			rr := f.do("PATCH", path, body)
			if rr.Code != 200 {
				t.Fatal(rr.Code, rr.Body.String())
			}
			var raw string
			if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE json_extract(payload,'$.action')=? AND json_extract(payload,'$.entity_id')=? ORDER BY seq DESC LIMIT 1`, action, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var event struct {
				Metadata struct{ Previous, Current map[string]any } `json:"metadata"`
			}
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				t.Fatal(err)
			}
			key := "allow_privileged_credentials"
			if kind == "crew" {
				key = "privileged"
			}
			if event.Metadata.Previous[key] != true || event.Metadata.Current[key] != false {
				t.Fatalf("missing before/after privilege facts: %s", raw)
			}
			if kind == "crew" {
				caps, ok := event.Metadata.Current["cap_add"].([]any)
				old, oldOK := event.Metadata.Previous["cap_add"].([]any)
				if !ok || len(caps) != 1 || caps[0] != "NET_BIND_SERVICE" || !oldOK || len(old) != 0 {
					t.Fatalf("missing capability values: %s", raw)
				}
			}
			if strings.Contains(raw, "never-copy-this") || strings.Contains(raw, "fixture-image") || strings.Contains(raw, "containerEnv") {
				t.Fatal("raw runtime config leaked into audit")
			}
		})
	}
}
