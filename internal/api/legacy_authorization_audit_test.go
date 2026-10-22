package api

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// F1: access-affecting writes that used to land only in the unchained
// audit_logs (several of them after commit, best effort) now append the
// chained journal in the same transaction as the change. Each case is run
// healthy (one chained entry with previous/current, one Settings Audit row,
// a valid chain) and with the journal insert aborted (HTTP 500, the change
// and the legacy row both rolled back, no post-commit notification).
func TestLegacyAuthorizationWritesAuditAtomically(t *testing.T) {
	type operation struct {
		method, path, body, action, entity string
		read                               func(*sql.DB) string
		before, after                      string
		wantPrevious                       bool
	}
	seedCrews := func(t *testing.T, f *authorizationAuditFixture) (string, string) {
		t.Helper()
		for _, id := range []string{"f1-crew-a", "f1-crew-b"} {
			execOrFatal(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, id, f.workspace, id, id)
		}
		return "f1-crew-a", "f1-crew-b"
	}
	seedLink := func(t *testing.T, f *authorizationAuditFixture, direction string) string {
		t.Helper()
		a, b := seedCrews(t, f)
		execOrFatal(t, f.db, `INSERT INTO crew_connections(id,workspace_id,from_crew_id,to_crew_id,direction) VALUES('f1-link',?,?,?,?)`, f.workspace, a, b, direction)
		return "f1-link"
	}
	scalar := func(query string, args ...any) func(*sql.DB) string {
		return func(db *sql.DB) string {
			var v sql.NullString
			if err := db.QueryRow(query, args...).Scan(&v); err != nil {
				return "error: " + err.Error()
			}
			return v.String
		}
	}
	cases := map[string]func(*testing.T, *authorizationAuditFixture) operation{
		"crew_link_create": func(t *testing.T, f *authorizationAuditFixture) operation {
			a, b := seedCrews(t, f)
			return operation{"POST", "/api/v1/crew-connections", `{"from_crew_id":"` + a + `","to_crew_id":"` + b + `","forward_file_access":"read"}`,
				"crew_link.create", "CREW_LINK", scalar(`SELECT CAST(COUNT(*) AS TEXT) FROM crew_connections WHERE from_crew_id=?`, a), "0", "1", false}
		},
		"crew_link_relink": func(t *testing.T, f *authorizationAuditFixture) operation {
			seedLink(t, f, "unidirectional")
			return operation{"POST", "/api/v1/crew-connections", `{"from_crew_id":"f1-crew-b","to_crew_id":"f1-crew-a"}`,
				"crew_link.update", "CREW_LINK", scalar(`SELECT direction FROM crew_connections WHERE id='f1-link'`), "unidirectional", "bidirectional", true}
		},
		"crew_link_delete": func(t *testing.T, f *authorizationAuditFixture) operation {
			id := seedLink(t, f, "bidirectional")
			return operation{"DELETE", "/api/v1/crew-connections/" + id, "",
				"crew_link.delete", "CREW_LINK", scalar(`SELECT CAST(COUNT(*) AS TEXT) FROM crew_connections WHERE id=?`, id), "1", "0", true}
		},
		"crew_link_file_access": func(t *testing.T, f *authorizationAuditFixture) operation {
			id := seedLink(t, f, "bidirectional")
			return operation{"PUT", "/api/v1/crew-connections/" + id + "/file-access", `{"requester_crew_id":"f1-crew-a","level":"read","expected_version":1}`,
				"crew_link.file_access", "CREW_LINK", scalar(`SELECT forward_file_access FROM crew_connections WHERE id=?`, id), "read_write", "read", true}
		},
		"artifact_creation_policy": func(t *testing.T, f *authorizationAuditFixture) operation {
			a, _ := seedCrews(t, f)
			return operation{"PUT", "/artifact-creation-policy", `{"enabled":true,"crew_ids":["` + a + `"],"base_version":0}`,
				"workspace.artifact_creation_policy.update", "WORKSPACE", scalar(`SELECT CAST(COUNT(*) AS TEXT) FROM workspace_artifact_creation_crews WHERE workspace_id=?`, f.workspace), "0", "1", true}
		},
		"credential_folder_policy": func(t *testing.T, f *authorizationAuditFixture) operation {
			execOrFatal(t, f.db, `INSERT INTO credential_folders(id,workspace_id,name,created_at,updated_at) VALUES('f1-folder',?,'Vault','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, f.workspace)
			return operation{"PATCH", "/api/v1/credential-folders/f1-folder", `{"reveal_policy":"admins","min_security_level":2}`,
				"credential_folder.update", "CREDENTIAL_FOLDER", scalar(`SELECT reveal_policy FROM credential_folders WHERE id='f1-folder'`), "inherit", "admins", true}
		},
		"routine_origin_binding_clear": func(t *testing.T, f *authorizationAuditFixture) operation {
			execOrFatal(t, f.db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('f1-origin',?,'f1-root','f1-chat','f1-message','{}','2026-10-19T00:00:00Z')`, f.workspace)
			execOrFatal(t, f.db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,chat_origin_id) VALUES('f1-routine',?,'f1-routine','Routine','{"name":"Routine","steps":[]}','h',0,'f1-origin')`, f.workspace)
			return operation{"POST", "/pipelines/f1-routine/origin-binding", `{"action":"clear","reason":"Reviewed by operator","expected_origin_id":"f1-origin"}`,
				"routine.origin_binding.clear", "pipeline", scalar(`SELECT chat_origin_id FROM pipelines WHERE id='f1-routine'`), "f1-origin", "", true}
		},
	}
	for name, setup := range cases {
		for _, fault := range []string{"healthy", "audit_abort"} {
			t.Run(name+"/"+fault, func(t *testing.T) {
				f := newAuthorizationAuditFixture(t)
				op := setup(t, f)
				if fault == "audit_abort" {
					execOrFatal(t, f.db, `CREATE TRIGGER stop_access_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(ABORT,'access audit unavailable'); END`)
				}
				rr := f.do(op.method, op.path, op.body)
				saved := op.read(f.db)
				var chained, legacy int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'audit.entity_%' AND json_extract(payload,'$.action')=?`, op.action).Scan(&chained); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=?`, op.action).Scan(&legacy); err != nil {
					t.Fatal(err)
				}
				if fault == "audit_abort" {
					if rr.Code != 500 || saved != op.before || chained != 0 || legacy != 0 || f.published.Load() != 0 {
						t.Fatalf("audit failure committed the change: HTTP%d state=%q want=%q chain=%d legacy=%d callbacks=%d body=%s",
							rr.Code, saved, op.before, chained, legacy, f.published.Load(), rr.Body.String())
					}
					return
				}
				if rr.Code >= 300 || saved != op.after {
					t.Fatalf("healthy calibration HTTP%d state=%q want=%q: %s", rr.Code, saved, op.after, rr.Body.String())
				}
				if chained != 1 || legacy != 1 || f.published.Load() != 1 {
					t.Fatalf("missing atomic dual audit: chain=%d legacy=%d callbacks=%d", chained, legacy, f.published.Load())
				}
				var actor, raw string
				if err := f.db.QueryRow(`SELECT actor_id,payload FROM journal_entries WHERE json_extract(payload,'$.action')=?`, op.action).Scan(&actor, &raw); err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Entity   string         `json:"entity_type"`
					Metadata map[string]any `json:"metadata"`
				}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				if actor != f.owner || payload.Entity != op.entity {
					t.Fatalf("wrong audit binding: actor=%s payload=%s", actor, raw)
				}
				if _, ok := payload.Metadata["current"]; !ok {
					t.Errorf("audit has no current value: %s", raw)
				}
				if _, ok := payload.Metadata["previous"]; ok != op.wantPrevious {
					t.Errorf("audit previous value present=%v, want %v: %s", ok, op.wantPrevious, raw)
				}
				for _, forbidden := range []string{`"token"`, `"password"`, `"secret"`} {
					if strings.Contains(raw, forbidden) {
						t.Errorf("private value in audit: %s", forbidden)
					}
				}
				f.checkChain(t)
			})
		}
	}
}
