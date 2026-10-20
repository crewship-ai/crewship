package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCrewMembershipAuditAtomic(t *testing.T) {
	for _, operation := range []string{"add", "remove", "set", "clear"} {
		for _, fault := range []string{"none", "ABORT", "IGNORE", "deferred_commit"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				var fk bool
				if err := f.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || !fk {
					t.Fatalf("fixture requires foreign keys: enabled=%v err=%v", fk, err)
				}
				crew := seedTestCrew(t, f.db, f.workspace)
				member := "crew-audit-member"
				method, path, body := "POST", "/api/v1/crews/"+crew+"/members", `{"user_id":"`+f.target+`"}`
				var before, after any
				after = map[string]any{"role": nil}
				action, status := "added", 201
				if operation != "add" {
					role := ""
					if operation == "clear" {
						role = "MANAGER"
					}
					seedCrewMemberRow(t, f.db, member, crew, f.target, role)
					before = map[string]any{"role": nil}
					if role != "" {
						before = map[string]any{"role": role}
					}
					method, path, status = "PATCH", path+"/"+member, 200
					body, action = `{"role":"MANAGER"}`, "role_changed"
					after = map[string]any{"role": "MANAGER"}
					if operation == "remove" {
						method, body, action, after = "DELETE", "", "removed", nil
					} else if operation == "clear" {
						body, after = `{"role":""}`, map[string]any{"role": nil}
					}
				}
				if fault == "deferred_commit" {
					execOrFatal(t, f.db, `CREATE TABLE crew_audit_commit_failure(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
					execOrFatal(t, f.db, `CREATE TRIGGER fail_crew_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN INSERT INTO crew_audit_commit_failure VALUES('absent-crew-audit-user'); END`)
				} else if fault != "none" {
					failure := `RAISE(IGNORE)`
					if fault == "ABORT" {
						failure = `RAISE(ABORT,'crew audit unavailable')`
					}
					execOrFatal(t, f.db, `CREATE TRIGGER fail_crew_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN SELECT `+failure+`; END`)
				}
				rr := f.do(method, path, body)
				var savedID string
				var savedRole sql.NullString
				err := f.db.QueryRow(`SELECT id,role FROM crew_members WHERE crew_id=? AND user_id=?`, crew, f.target).Scan(&savedID, &savedRole)
				var stored any
				if err == nil {
					stored = map[string]any{"role": nil}
					if savedRole.Valid {
						stored = map[string]any{"role": savedRole.String}
					}
				} else if err != sql.ErrNoRows {
					t.Fatal(err)
				}
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if fault != "none" {
					if rr.Code != 500 || !reflect.DeepEqual(stored, before) || count != 0 || f.published.Load() != 0 {
						t.Errorf("audit failure committed crew mutation: HTTP%d stored=%v want=%v audits=%d notified=%d", rr.Code, stored, before, count, f.published.Load())
					}
				} else {
					if rr.Code != status || !reflect.DeepEqual(stored, after) {
						t.Fatalf("valid production mutation failed: HTTP%d stored=%v want=%v", rr.Code, stored, after)
					}
					if count != 1 {
						t.Fatalf("committed crew mutation missing canonical old/new audit: count=%d", count)
					}
					var actor, savedCrew, raw string
					if err := f.db.QueryRow(`SELECT actor_id,crew_id,payload FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&actor, &savedCrew, &raw); err != nil {
						t.Fatal(err)
					}
					var payload map[string]any
					if err := json.Unmarshal([]byte(raw), &payload); err != nil {
						t.Fatal(err)
					}
					if operation == "add" {
						member = savedID
					}
					want := map[string]any{"subject_user_id": f.target, "member_id": member, "action": action, "previous_membership": before, "membership": after}
					if actor != f.owner || savedCrew != crew || !reflect.DeepEqual(payload, want) || f.published.Load() != 1 {
						t.Errorf("wrong crew audit: actor=%s crew=%s payload=%s notified=%d", actor, savedCrew, raw, f.published.Load())
					}
				}
				f.checkChain(t)
			})
		}
	}
}

func TestCrewMembershipAuditFinalState(t *testing.T) {
	for _, operation := range []string{"add", "remove", "set"} {
		t.Run(operation, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			crew := seedTestCrew(t, f.db, f.workspace)
			method, path, body := "POST", "/api/v1/crews/"+crew+"/members", `{"user_id":"`+f.target+`"}`
			if operation != "add" {
				seedCrewMemberRow(t, f.db, "crew-audit-final", crew, f.target, "")
				method, path, body = "PATCH", path+"/crew-audit-final", `{"role":"MANAGER"}`
			}
			if operation == "remove" {
				method, body = "DELETE", ""
				execOrFatal(t, f.db, `CREATE TRIGGER undo_crew_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN INSERT INTO crew_members(id,crew_id,user_id) VALUES('crew-audit-final','`+crew+`','`+f.target+`'); END`)
			} else {
				execOrFatal(t, f.db, `CREATE TRIGGER undo_crew_audit AFTER INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN UPDATE crew_members SET role='VIEWER' WHERE crew_id='`+crew+`' AND user_id='`+f.target+`'; END`)
			}
			rr := f.do(method, path, body)
			var count, audits int
			var role sql.NullString
			if err := f.db.QueryRow(`SELECT COUNT(*),MAX(role) FROM crew_members WHERE crew_id=? AND user_id=?`, crew, f.target).Scan(&count, &role); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			want := 1
			if operation == "add" {
				want = 0
			}
			if rr.Code != 500 || count != want || role.Valid || audits != 0 || f.published.Load() != 0 {
				t.Fatalf("final state was not acknowledged: HTTP%d members=%d role=%v audits=%d notify=%d", rr.Code, count, role, audits, f.published.Load())
			}
			f.checkChain(t)
		})
	}
}

func TestCrewMembershipRemovalRejectsReplacementIdentity(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "healthy"
		if replacement {
			name = "replacement_new_id"
		}
		t.Run(name, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			crew := seedTestCrew(t, f.db, f.workspace)
			seedCrewMemberRow(t, f.db, "crew-identity-original", crew, f.target, "")
			if replacement {
				// The old membership has been deleted, so this INSERT is
				// legal under the real unique (crew_id,user_id) constraint.
				execOrFatal(t, f.db, `CREATE TRIGGER replace_removed_membership AFTER INSERT ON journal_entries WHEN NEW.entry_type='crew.member_changed' BEGIN INSERT INTO crew_members(id,crew_id,user_id) VALUES('crew-identity-replacement','`+crew+`','`+f.target+`'); END`)
			}
			rr := f.do("DELETE", "/api/v1/crews/"+crew+"/members/crew-identity-original", "")
			var count, events int
			var id string
			if err := f.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(id),'') FROM crew_members WHERE crew_id=? AND user_id=?`, crew, f.target).Scan(&count, &id); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if replacement {
				if rr.Code != 500 || count != 1 || id != "crew-identity-original" || events != 0 || f.published.Load() != 0 {
					t.Errorf("replacement identity escaped final removal ACK: HTTP%d members=%d id=%s audits=%d notified=%d", rr.Code, count, id, events, f.published.Load())
				}
			} else if rr.Code != 200 || count != 0 || events != 1 || f.published.Load() != 1 {
				t.Fatalf("healthy production removal failed: HTTP%d members=%d audits=%d notified=%d", rr.Code, count, events, f.published.Load())
			}
			f.checkChain(t)
		})
	}
}

func TestCrewMembershipMutationRechecksCurrentActor(t *testing.T) {
	for _, operation := range []string{"add", "remove", "set"} {
		for _, change := range []string{"demoted", "suspended", "removed", "restricted_elsewhere", "crew_deleted", "workspace_deleted", "canceled_query"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				crew := seedTestCrew(t, f.db, f.workspace)
				seedCrewMemberRow(t, f.db, "crew-stale-member", crew, f.target, "")
				method, body := "POST", `{"user_id":"`+f.target+`"}`
				if operation == "add" {
					execOrFatal(t, f.db, `DELETE FROM crew_members WHERE id='crew-stale-member'`)
				} else if operation == "remove" {
					method, body = "DELETE", ""
				} else {
					method, body = "PATCH", `{"role":"MANAGER"}`
				}
				// The identity/context represents a previously admitted real
				// member. Change durable authority before calling the handler.
				req := withWorkspaceUser(httptest.NewRequest(method, "/x", strings.NewReader(body)), f.owner, f.workspace, "OWNER")
				req.SetPathValue("crewId", crew)
				req.SetPathValue("memberId", "crew-stale-member")
				want := 403
				switch change {
				case "demoted":
					execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.owner)
				case "suspended":
					execOrFatal(t, f.db, `UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id=?`, f.owner)
				case "removed":
					execOrFatal(t, f.db, `DELETE FROM workspace_members WHERE user_id=?`, f.owner)
				case "restricted_elsewhere":
					execOrFatal(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id=?`, f.owner)
					execOrFatal(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('crew-audit-other','Other','crew-audit-other')`)
					execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-audit-guest','crew-audit-other',?,'MEMBER','restricted')`, f.owner)
				case "crew_deleted":
					execOrFatal(t, f.db, `UPDATE crews SET deleted_at='2026-10-19T00:00:00Z' WHERE id=?`, crew)
					want = 404
				case "workspace_deleted":
					execOrFatal(t, f.db, `UPDATE workspaces SET deleted_at='2026-10-19T00:00:00Z' WHERE id=?`, f.workspace)
				case "canceled_query":
					ctx, cancel := context.WithCancel(req.Context())
					cancel()
					req = req.WithContext(ctx)
					want = 500 // operational errors must not become policy denials
				}
				h := NewCrewHandler(f.db, newTestLogger())
				rr := httptest.NewRecorder()
				switch operation {
				case "add":
					h.AddMember(rr, req)
				case "remove":
					h.RemoveMember(rr, req)
				case "set":
					h.UpdateMemberRole(rr, req)
				}
				var count int
				var role sql.NullString
				if err := f.db.QueryRow(`SELECT COUNT(*),MAX(role) FROM crew_members WHERE crew_id=? AND user_id=?`, crew, f.target).Scan(&count, &role); err != nil {
					t.Fatal(err)
				}
				wantMembers := 1
				if operation == "add" {
					wantMembers = 0
				}
				if rr.Code != want || count != wantMembers || role.Valid || f.published.Load() != 0 {
					t.Fatalf("stale actor affected membership: HTTP%d want%d members=%d role=%v notify=%d", rr.Code, want, count, role, f.published.Load())
				}
				f.checkChain(t)
			})
		}
	}
}

func TestCrewMembershipMutationBodyRaceAndNoop(t *testing.T) {
	for _, operation := range []string{"add", "set"} {
		t.Run(operation, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			crew := seedTestCrew(t, f.db, f.workspace)
			path, method, body := "/api/v1/crews/"+crew+"/members", "POST", `{"user_id":"`+f.target+`"}`
			if operation == "set" {
				seedCrewMemberRow(t, f.db, "crew-body-race", crew, f.target, "")
				path, method, body = path+"/crew-body-race", "PATCH", `{"role":"MANAGER"}`
			}
			read := false
			f.beforeBody = func() {
				read = true
				execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.owner)
			}
			rr := f.do(method, path, body)
			var audits int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if !read || rr.Code != 403 || audits != 0 || f.published.Load() != 0 {
				t.Fatalf("body race passed: body=%v HTTP%d audit=%d notified=%d", read, rr.Code, audits, f.published.Load())
			}
		})
	}
	t.Run("unchanged_override", func(t *testing.T) {
		f := newAuthorizationAuditFixture(t)
		crew := seedTestCrew(t, f.db, f.workspace)
		seedCrewMemberRow(t, f.db, "crew-noop", crew, f.target, "MANAGER")
		rr := f.do("PATCH", "/api/v1/crews/"+crew+"/members/crew-noop", `{"role":"MANAGER"}`)
		var audits int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='crew.member_changed'`).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || audits != 0 || f.published.Load() != 0 {
			t.Fatalf("unchanged override emitted audit: HTTP%d audit=%d notified=%d", rr.Code, audits, f.published.Load())
		}
	})
}
