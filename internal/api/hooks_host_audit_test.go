package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
)

func TestHostHookAuthorityMutationRollsBackWhenAuditFails(t *testing.T) {
	for _, failure := range []string{"chain-abort", "chain-ignore", "projection-ignore"} {
		for _, operation := range []string{"create", "update", "downgrade", "enable", "disable", "delete"} {
			t.Run(failure+"/"+operation, func(t *testing.T) {
				f := newInstanceFixture(t)
				enabled := operation == "disable"
				body := `{"event":"pre_agent_start","handler_kind":"shell","handler_config":{"command":"true"},"enabled":false}`
				if enabled {
					body = `{"event":"pre_agent_start","handler_kind":"shell","handler_config":{"command":"true"},"enabled":true}`
				}
				created := f.do(f.boss, http.MethodPost, "/api/v1/hooks?workspace_id=ws-old", body)
				wantCode(t, created, http.StatusCreated, "initial audited host configuration")
				var row struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil || row.ID == "" {
					t.Fatal(created.Body.String())
				}
				before, err := hooks.Get(t.Context(), f.db, "ws-old", row.ID)
				if err != nil || !hooks.HostAuthorized(*before) {
					t.Fatalf("missing initial approval: %v", err)
				}
				var oldAudit int
				if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id=?`, row.ID).Scan(&oldAudit); err != nil {
					t.Fatal(err)
				}
				fault := `CREATE TRIGGER fail_host_chain BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(ABORT,'host audit unavailable'); END`
				if failure == "chain-ignore" {
					fault = `CREATE TRIGGER fail_host_chain BEFORE INSERT ON journal_entries WHEN NEW.entry_type LIKE 'audit.entity_%' BEGIN SELECT RAISE(IGNORE); END`
				}
				if failure == "projection-ignore" {
					fault = `CREATE TRIGGER fail_host_projection BEFORE INSERT ON audit_logs WHEN NEW.entity_type='hook' BEGIN SELECT RAISE(IGNORE); END`
				}
				if _, err := f.db.Exec(fault); err != nil {
					t.Fatal(err)
				}
				method, path, request := http.MethodPost, "/api/v1/hooks", body
				switch operation {
				case "update":
					method, path, request = http.MethodPatch, "/api/v1/hooks/"+row.ID, `{"handler_config":{"command":"changed"}}`
				case "downgrade":
					method, path, request = http.MethodPatch, "/api/v1/hooks/"+row.ID, `{"handler_kind":"http","handler_config":{"url":"https://example.test"}}`
				case "enable", "disable":
					path = "/api/v1/hooks/" + row.ID + "/" + operation
					request = `{}`
				case "delete":
					method, path, request = http.MethodDelete, "/api/v1/hooks/"+row.ID, ""
				}
				wantCode(t, f.do(f.boss, method, path+"?workspace_id=ws-old", request), http.StatusInternalServerError, "chain failure")
				after, err := hooks.Get(t.Context(), f.db, "ws-old", row.ID)
				if err != nil || after == nil || after.HandlerKind != before.HandlerKind || after.Enabled != before.Enabled || after.HostAuthorization != before.HostAuthorization || after.HandlerConfig["command"] != before.HandlerConfig["command"] {
					t.Fatalf("host mutation escaped rollback: before=%+v after=%+v err=%v", before, after, err)
				}
				var count, audits int
				if err := f.db.QueryRow(`SELECT count(*) FROM hooks_config`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id=?`, row.ID).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if count != 1 || audits != oldAudit {
					t.Fatalf("partial grant/audit: hooks=%d audits=%d want=%d", count, audits, oldAudit)
				}
			})
		}
	}
}

func TestHostHookAuthorityAuditBindsActorAndRevision(t *testing.T) {
	f := newInstanceFixture(t)
	result := f.do(f.boss, http.MethodPost, "/api/v1/hooks?workspace_id=ws-old", `{"event":"pre_agent_start","handler_kind":"shell","handler_config":{"command":"true"},"enabled":false}`)
	wantCode(t, result, http.StatusCreated, "host grant")
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	current, err := hooks.Get(t.Context(), f.db, "ws-old", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var actor, approval string
	if err := f.db.QueryRow(`SELECT actor_id,json_extract(payload,'$.metadata.after_authorization') FROM journal_entries WHERE workspace_id='ws-old' AND json_extract(payload,'$.entity_id')=? AND json_extract(payload,'$.action')='hook.create'`, row.ID).Scan(&actor, &approval); err != nil {
		t.Fatal(err)
	}
	if actor != "boss" || approval == "" || approval != current.HostAuthorization {
		t.Fatalf("wrong authority audit: actor=%q approval=%q", actor, approval)
	}
}
