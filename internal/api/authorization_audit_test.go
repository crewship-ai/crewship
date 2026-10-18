package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
)

type authorizationAuditFixture struct {
	db                               *sql.DB
	owner, workspace, target, member string
	key                              []byte
	published                        atomic.Int64
	beforeBody                       func()
	do                               func(string, string, string) *httptest.ResponseRecorder
}

// Run after middleware admission, at the handler's first body read.
type authorizationAuditBody struct {
	reader *strings.Reader
	before func()
}

func (b *authorizationAuditBody) Read(p []byte) (int, error) {
	if b.before != nil {
		f := b.before
		b.before = nil
		f()
	}
	return b.reader.Read(p)
}

func newAuthorizationAuditFixture(t *testing.T) *authorizationAuditFixture {
	t.Helper()
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	target := seedMemberWithCapabilities(t, db, ws, "MEMBER", `["chat","routine.create"]`, "audit-subject")
	f := &authorizationAuditFixture{db: db, owner: owner, workspace: ws, target: target, member: "test-mem-audit-subject", key: []byte("isolated-administration-journal-key")}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: f.key, FlushInterval: time.Hour})
	t.Cleanup(func() { writer.Close() })
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: ws, Type: journal.EntryInvitationCreated, ActorType: journal.ActorUser, ActorID: owner, Summary: "Existing chain entry"}); err != nil {
		t.Fatal(err)
	}
	writer.AddCommitObserver(func([]journal.Entry) { f.published.Add(1) })
	secret := "administration-audit-test-signing-key"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), owner, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(owner, session.ID, "Owner", "owner@test.invalid")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	f.do = func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/workspaces/"+ws+path, &authorizationAuditBody{reader: strings.NewReader(body), before: f.beforeBody})
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", ws)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	return f
}

func (f *authorizationAuditFixture) checkChain(t *testing.T) {
	t.Helper()
	result, err := journal.VerifyChainWithKey(t.Context(), f.db, f.workspace, f.key)
	if err != nil || !result.OK {
		t.Fatalf("configured chain invalid: %+v, %v", result, err)
	}
}

func TestAuthorizationAuditCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		after      []string
	}{
		{"set", `{"set":["chat","issue.create"]}`, []string{"chat", "issue.create"}},
		{"grant", `{"grant":["issue.create"]}`, []string{"chat", "issue.create", "routine.create"}},
		{"revoke", `{"revoke":["routine.create"]}`, []string{"chat"}},
		{"preset", `{"preset":"chat"}`, []string{"chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			rr := f.do("PATCH", "/members/"+f.target+"/capabilities", tc.body)
			if rr.Code != 200 {
				t.Fatalf("mutation status %d: %s", rr.Code, rr.Body.String())
			}
			var raw, actor string
			var count int
			err := f.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(payload),''),COALESCE(MAX(actor_id),'') FROM journal_entries WHERE workspace_id=? AND entry_type='member.capabilities_changed'`, f.workspace).Scan(&count, &raw, &actor)
			if err != nil || count != 1 {
				t.Fatalf("successful capability mutation missing chained event: count=%d err=%v", count, err)
			}
			var response capabilitiesGetResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(response.Capabilities, tc.after) {
				t.Fatalf("wrong saved capabilities: %v", response.Capabilities)
			}
			var payload map[string]any
			if err = json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			wantPayload := map[string]any{"subject_user_id": f.target, "action": "capabilities_changed"}
			if actor != f.owner || !reflect.DeepEqual(payload, wantPayload) {
				t.Fatalf("audit identity or capability privacy changed: actor=%s payload=%s", actor, raw)
			}

			f.checkChain(t)
			if f.published.Load() != 1 {
				t.Fatal("committed audit was not published exactly once")
			}
			rr = f.do("PATCH", "/members/"+f.target+"/capabilities", tc.body)
			if rr.Code != 200 {
				t.Fatal(rr.Code)
			}
			if f.published.Load() != 1 {
				t.Fatal("no-op mutation emitted another event")
			}
			rr = f.do("PATCH", "/members/"+f.owner+"/capabilities", tc.body)
			if rr.Code != 403 {
				t.Fatal("self mutation did not deny", rr.Code)
			}
			if f.published.Load() != 1 {
				t.Fatal("denied mutation emitted event")
			}
		})
	}
}

func TestAuthorizationAuditCapabilitiesRollback(t *testing.T) {
	for _, fault := range []string{"ABORT", "IGNORE"} {
		t.Run(fault, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			action := `RAISE(IGNORE)`
			if fault == "ABORT" {
				action = `RAISE(ABORT,'audit unavailable')`
			}
			execOrFatal(t, f.db, `CREATE TRIGGER fail_capability_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.capabilities_changed' BEGIN SELECT `+action+`; END`)
			rr := f.do("PATCH", "/members/"+f.target+"/capabilities", `{"grant":["issue.create"]}`)
			if rr.Code != 500 {
				t.Fatalf("audit failure must refuse capability mutation: got %d", rr.Code)
			}
			var raw string
			if err := f.db.QueryRow(`SELECT capabilities FROM workspace_members WHERE id=?`, f.member).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw != `["chat","routine.create"]` {
				t.Fatal("audit failure committed capabilities", raw)
			}
			if f.published.Load() != 0 {
				t.Fatal("rolled-back audit was published")
			}
			f.checkChain(t)
		})
	}
}

func TestAuthorizationAuditMemberRemoval(t *testing.T) {
	for _, fault := range []string{"none", "ABORT", "IGNORE", "DELETE_IGNORE"} {
		t.Run(fault, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			crew := seedTestCrew(t, f.db, f.workspace)
			execOrFatal(t, f.db, `INSERT INTO crew_members(id,crew_id,user_id,role) VALUES('audit-crew-member',?,?,'MEMBER')`, crew, f.target)
			if fault == "DELETE_IGNORE" {
				execOrFatal(t, f.db, `CREATE TRIGGER ignore_member_delete BEFORE DELETE ON workspace_members WHEN OLD.id='test-mem-audit-subject' BEGIN SELECT RAISE(IGNORE); END`)
			} else if fault != "none" {
				action := `RAISE(IGNORE)`
				if fault == "ABORT" {
					action = `RAISE(ABORT,'audit unavailable')`
				}
				execOrFatal(t, f.db, `CREATE TRIGGER fail_removal_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.removed' BEGIN SELECT `+action+`; END`)
			}
			rr := f.do("DELETE", "/members/"+f.member, "")
			if fault == "none" {
				if rr.Code != 200 {
					t.Fatalf("member removal status %d: %s", rr.Code, rr.Body.String())
				}
				var count int
				var payload, actor string
				err := f.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(payload),''),COALESCE(MAX(actor_id),'') FROM journal_entries WHERE workspace_id=? AND entry_type='member.removed'`, f.workspace).Scan(&count, &payload, &actor)
				if err != nil || count != 1 || actor != f.owner || !strings.Contains(payload, f.target) {
					t.Fatalf("member removal missing chained identity event: count=%d actor=%s payload=%s err=%v", count, actor, payload, err)
				}
				if f.published.Load() != 1 {
					t.Fatal("removal audit missing post-commit publish")
				}
				rr = f.do("DELETE", "/members/"+f.member, "")
				if rr.Code != 404 || f.published.Load() != 1 {
					t.Fatal("missing member retry emitted audit", rr.Code)
				}
			} else {
				if rr.Code != 500 {
					t.Fatalf("audit failure must roll back membership removal, got %d", rr.Code)
				}
				if f.published.Load() != 0 {
					t.Fatal("rollback published audit")
				}
			}
			var members, crews int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.target, f.workspace).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE user_id=? AND crew_id=?`, f.target, crew).Scan(&crews); err != nil {
				t.Fatal(err)
			}
			want := 1
			if fault == "none" {
				want = 0
			}
			if members != want || crews != want {
				t.Fatalf("partial removal: members=%d crews=%d want=%d", members, crews, want)
			}
			f.checkChain(t)
		})
	}
}

func TestAuthorizationAuditCapabilitiesRechecksCaller(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"demoted", `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`},
		{"membership_removed", `DELETE FROM workspace_members WHERE user_id=?`},
		{"suspended", `UPDATE users SET suspended_at='2026-10-18T00:00:00Z' WHERE id=?`},
		{"workspace_deleted", `UPDATE workspaces SET deleted_at='2026-10-18T00:00:00Z' WHERE id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthorizationAuditFixture(t)
			read := false
			f.beforeBody = func() { read = true; execOrFatal(t, f.db, tc.sql, f.owner) }
			rr := f.do("PATCH", "/members/"+f.target+"/capabilities", `{"grant":["issue.create"]}`)
			if !read {
				t.Fatal("body-read barrier not reached")
			}
			if rr.Code != 403 {
				t.Fatalf("stale caller changed capabilities: status %d", rr.Code)
			}
			var caps string
			if err := f.db.QueryRow(`SELECT capabilities FROM workspace_members WHERE id=?`, f.member).Scan(&caps); err != nil {
				t.Fatal(err)
			}
			if caps != `["chat","routine.create"]` || f.published.Load() != 0 {
				t.Fatalf("stale caller committed mutation: caps=%s published=%d", caps, f.published.Load())
			}
			f.checkChain(t)
		})
	}
}
