package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
)

type crewManageHTTPFixture struct {
	db                                            *sql.DB
	owner, user, subject, workspace, crew, member string
	beforeBody                                    func()
	do                                            func(string, string, string, string) *httptest.ResponseRecorder
}

func newCrewManageHTTPFixture(t *testing.T) *crewManageHTTPFixture {
	t.Helper()
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	user := seedMemberWithCapabilities(t, db, workspace, "MEMBER", `[]`, "crew-manager")
	subject := seedMemberWithCapabilities(t, db, workspace, "MEMBER", `[]`, "crew-subject")
	f := &crewManageHTTPFixture{db: db, owner: owner, user: user, subject: subject, workspace: workspace, crew: seedTestCrew(t, db, workspace)}
	if err := db.QueryRow(`SELECT id FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&f.member); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("crew-manage-test-audit-chain-key"), FlushInterval: time.Hour})
	t.Cleanup(func() { writer.Close() })
	const secret = "crew-manage-http-test-signing-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, id := range []string{owner, user, subject} {
		session, err := sessions.NewDBStore(db).Create(t.Context(), id, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		token, err := validator.IssueAccessToken(id, session.ID, "Test", "test@example.invalid")
		if err != nil {
			t.Fatal(err)
		}
		tokens[id] = token
	}
	f.do = func(actor, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, &authorizationAuditBody{reader: strings.NewReader(body), before: f.beforeBody})
		req.Header.Set("Authorization", "Bearer "+tokens[actor])
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	return f
}

func (f *crewManageHTTPFixture) grant(t *testing.T, rights []access.Right, mode string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/workspaces/" + f.workspace + "/members/" + f.member + "/access"
	rr := f.do(f.owner, "GET", path, "")
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var policy access.Policy
	if err := json.Unmarshal(rr.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	policy.Mode, policy.Rights = mode, rights
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return f.do(f.owner, "PUT", path, string(raw))
}

func TestTrustedCrewManageHTTPAuthorityAndRoleBoundary(t *testing.T) {
	f := newCrewManageHTTPFixture(t)
	path := "/api/v1/crews/" + f.crew
	if rr := f.do(f.user, "PATCH", path, `{"name":"Denied"}`); rr.Code != 403 {
		t.Fatal("ungranted", rr.Code)
	}
	if rr := f.grant(t, []access.Right{{Kind: "crew", ID: f.crew, Operation: "manage"}}, "trusted"); rr.Code != 200 {
		t.Fatal("grant", rr.Code, rr.Body.String())
	}
	for _, method := range []string{"PATCH", "PUT"} {
		if rr := f.do(f.user, method, path, `{"name":"Managed crew"}`); rr.Code != 200 {
			t.Fatal("settings", rr.Code, rr.Body.String())
		}
	}
	memberPath := path + "/members"
	if rr := f.do(f.user, "POST", memberPath, `{"user_id":"`+f.subject+`","role":"MEMBER"}`); rr.Code != 403 {
		t.Fatal("role via add", rr.Code, rr.Body.String())
	}
	if rr := f.do(f.user, "POST", memberPath, `{"user_id":"`+f.subject+`"}`); rr.Code != 201 {
		t.Fatal("add", rr.Code, rr.Body.String())
	}
	var membership string
	if err := f.db.QueryRow(`SELECT id FROM crew_members WHERE crew_id=? AND user_id=?`, f.crew, f.subject).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	if rr := f.do(f.user, "PATCH", memberPath+"/"+membership, `{"role":"MANAGER"}`); rr.Code != 403 {
		t.Fatal("role update", rr.Code)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"DELETE", path, ""}, {"PUT", path + "/budget", `{"monthly_budget_usd":1}`},
		{"PATCH", path, `{"allow_private_endpoints":true}`},
		{"PATCH", "/api/v1/workspaces/" + f.workspace, `{"name":"Denied workspace"}`},
	} {
		if rr := f.do(f.user, tc.method, tc.path, tc.body); rr.Code != 403 {
			t.Fatalf("extra authority %s %s: %d %s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
	if rr := f.do(f.user, "DELETE", memberPath+"/"+membership, ""); rr.Code != 200 {
		t.Fatal("remove", rr.Code, rr.Body.String())
	}
	if rr := f.grant(t, []access.Right{}, "restricted"); rr.Code != 422 {
		t.Fatal("downshift requires explicit revoke", rr.Code, rr.Body.String())
	}
	if rr := f.grant(t, []access.Right{}, "trusted"); rr.Code != 200 {
		t.Fatal("revoke", rr.Code, rr.Body.String())
	}
	if rr := f.do(f.user, "PATCH", path, `{"name":"Denied after revoke"}`); rr.Code != 403 {
		t.Fatal("revoke ineffective", rr.Code)
	}
	var name string
	if err := f.db.QueryRow(`SELECT name FROM crews WHERE id=?`, f.crew).Scan(&name); err != nil || name != "Managed crew" {
		t.Fatal(name, err)
	}
}

func TestTrustedCrewManageHTTPRechecksWriterAndAudit(t *testing.T) {
	for _, operation := range []string{"settings", "add"} {
		for _, fault := range []string{"revoke_before_writer", "issuer_before_writer", "audit_failure", "revoke_during_audit"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				f := newCrewManageHTTPFixture(t)
				if rr := f.grant(t, []access.Right{{Kind: "crew", ID: f.crew, Operation: "manage"}}, "trusted"); rr.Code != 200 {
					t.Fatal(rr.Code, rr.Body.String())
				}
				method, path, body := "PATCH", "/api/v1/crews/"+f.crew, `{"name":"Must roll back"}`
				if operation == "add" {
					method, path, body = "POST", path+"/members", `{"user_id":"`+f.subject+`"}`
				}
				switch fault {
				case "revoke_before_writer":
					f.beforeBody = func() { execOrFatal(t, f.db, `DELETE FROM access_grants WHERE member_id=?`, f.member) }
				case "issuer_before_writer":
					f.beforeBody = func() { execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.owner) }
				case "audit_failure":
					execOrFatal(t, f.db, `CREATE TRIGGER fail_manage_audit BEFORE INSERT ON journal_entries BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
				case "revoke_during_audit":
					execOrFatal(t, f.db, `CREATE TRIGGER revoke_manage_audit AFTER INSERT ON journal_entries BEGIN DELETE FROM access_grants; END`)
				}
				rr := f.do(f.user, method, path, body)
				if rr.Code != 403 && rr.Code != 500 {
					t.Fatal("stale authority committed", rr.Code, rr.Body.String())
				}
				var name string
				var members, logs int
				if err := f.db.QueryRow(`SELECT name FROM crews WHERE id=?`, f.crew).Scan(&name); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT count(*) FROM crew_members WHERE crew_id=?`, f.crew).Scan(&members); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='crew.update'`).Scan(&logs); err != nil {
					t.Fatal(err)
				}
				if name != "Test Crew" || members != 0 || logs != 0 {
					t.Fatalf("mutation escaped rollback: name=%s members=%d logs=%d", name, members, logs)
				}
			})
		}
	}
}
