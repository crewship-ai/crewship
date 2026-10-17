package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

type presetApplyFixture struct {
	db               *sql.DB
	owner, workspace string
	store            access.Store
	handler          *WorkspaceHandler
	request          access.PresetRequest
}

func newPresetApplyFixture(t *testing.T) presetApplyFixture {
	t.Helper()
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('apply-subject','subject@apply.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('apply-membership',?,'apply-subject','MEMBER','restricted')`, workspace)
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('apply-resource',?,'Resource','apply-resource'),('apply-other',?,'Other','apply-other')`, workspace, workspace)
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "apply-subject", workspace)
	if err != nil {
		t.Fatal(err)
	}
	handler := newWsHandlerForTest(t)
	handler.db = db
	return presetApplyFixture{db, owner, workspace, store, handler, access.PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: access.PresetResource{Kind: "project", ID: "apply-resource"}, PresetID: "project-levels", Version: 1, Level: "view", Reissue: []access.Right{}}}
}
func (f presetApplyFixture) preview(t *testing.T) access.PresetPreview {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	p, err := f.store.PlanPresetIn(t.Context(), tx, f.owner, "apply-subject", f.workspace, f.request)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f presetApplyFixture) apply(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(body)), f.owner, f.workspace, "OWNER")
	req.SetPathValue("workspaceId", f.workspace)
	req.SetPathValue("memberId", f.request.MembershipID)
	w := httptest.NewRecorder()
	f.handler.ApplyAccessPreset(w, req)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("apply response may be cached")
	}
	return w
}
func applyBody(t *testing.T, p access.PresetPreview) string {
	t.Helper()
	b, err := json.Marshal(presetApplyRequest{p.Selection, p.Digest})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestPresetApplyRollbackOnAuditOrSelectionFailure(t *testing.T) {
	for name, trigger := range map[string]string{
		"audit":     `CREATE TRIGGER reject_preset_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`,
		"selection": `CREATE TRIGGER reject_preset_selection BEFORE INSERT ON access_preset_selections BEGIN SELECT RAISE(ABORT,'synthetic selection failure'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPresetApplyFixture(t)
			before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			p := f.preview(t)
			execOrFatal(t, f.db, trigger)
			w := f.apply(t, applyBody(t, p))
			if w.Code != 500 {
				t.Fatalf("atomic failure=%d %s", w.Code, w.Body.String())
			}
			after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial canonical/metadata commit: before=%+v after=%+v err=%v", before, after, err)
			}
			for _, table := range []string{"journal_entries", "access_preset_selections"} {
				var count int
				if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE workspace_id=?`, f.workspace).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial %s commit:%d %v", table, count, err)
				}
			}
		})
	}
}
func TestPresetApplyRejectsStaleConsentWithoutWrites(t *testing.T) {
	for name, mutation := range map[string]func(*testing.T, presetApplyFixture){
		"resource": func(t *testing.T, f presetApplyFixture) {
			execOrFatal(t, f.db, `UPDATE projects SET updated_at='2026-10-17T12:00:00Z' WHERE id='apply-resource'`)
		},
		"role-floor": func(t *testing.T, f presetApplyFixture) {
			execOrFatal(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE id='apply-membership'`)
		},
		"digest": func(t *testing.T, f presetApplyFixture) {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPresetApplyFixture(t)
			p := f.preview(t)
			mutation(t, f)
			if name == "digest" {
				p.Digest = strings.Repeat("0", 64)
			}
			before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			w := f.apply(t, applyBody(t, p))
			if w.Code != 409 {
				t.Fatalf("stale consent=%d %s", w.Code, w.Body.String())
			}
			after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("conflict changed policy:%+v %v", after, err)
			}
		})
	}
}
func TestPresetApplyStrictInput(t *testing.T) {
	f := newPresetApplyFixture(t)
	valid := applyBody(t, f.preview(t))
	for name, body := range map[string]string{"unknown": strings.TrimSuffix(valid, "}") + `,"actor":"forged"}`, "trailing": valid + ` {}`, "nested": strings.Replace(valid, `"level":"view"`, `"level":"view","surprise":true`, 1), "oversized": strings.Repeat(" ", 256<<10) + valid, "missing-digest": strings.Replace(valid, `"confirmation_digest":"`, `"confirmation_digest":"invalid-`, 1)} {
		t.Run(name, func(t *testing.T) {
			w := f.apply(t, body)
			if w.Code != 400 {
				t.Fatalf("input=%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPresetApplyPreservesCanonicalRowsAndExplicitReissue(t *testing.T) {
	f := newPresetApplyFixture(t)
	execOrFatal(t, f.db, `INSERT INTO users(id,email) VALUES('apply-issuer','issuer@apply.invalid')`)
	execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('apply-issuer-membership',?,'apply-issuer','ADMIN')`, f.workspace)
	m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "project", ID: "apply-resource", Operation: "list"}, {Kind: "project", ID: "apply-resource", Operation: "read"}, {Kind: "project", ID: "apply-resource", Operation: "manage"}, {Kind: "project", ID: "apply-resource", Operation: "delete_issues"}, {Kind: "project", ID: "apply-other", Operation: "read"}}
	m, err = f.store.Replace(t.Context(), "apply-issuer", "apply-subject", f.workspace, "restricted", m, rights)
	if err != nil {
		t.Fatal(err)
	}
	f.request.Revision = m.Revision
	execOrFatal(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE id='apply-issuer-membership'`)
	m, err = f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	f.request.Revision = m.Revision
	snapshot := func() map[string]string {
		t.Helper()
		rows, err := f.db.QueryContext(t.Context(), `SELECT COALESCE(project_id,routine_id,agent_id)||':'||operation,json_object('id',id,'issuer',created_by,'created_at',created_at) FROM access_grants WHERE member_id=?`, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			result[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	w := f.apply(t, applyBody(t, f.preview(t)))
	if w.Code != 200 {
		t.Fatalf("apply=%d %s", w.Code, w.Body.String())
	}
	after := snapshot()
	for _, key := range []string{"apply-resource:list", "apply-resource:read", "apply-resource:delete_issues", "apply-other:read"} {
		if before[key] != after[key] {
			t.Fatalf("retained canonical row rewritten:%s %s→%s", key, before[key], after[key])
		}
	}
	if _, exists := after["apply-resource:manage"]; exists {
		t.Fatal("managed downgrade retained Manage")
	}
	if err := f.store.Check(t.Context(), "apply-subject", f.workspace, rights[1]); err == nil {
		t.Fatal("preset silently revived inert issuer")
	}
	m, err = f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	f.request.Revision = m.Revision
	f.request.Reissue = []access.Right{rights[1]}
	p := f.preview(t)
	if len(p.Changes) != 2 {
		t.Fatalf("managed preview:%+v", p.Changes)
	}
	w = f.apply(t, applyBody(t, p))
	if w.Code != 200 {
		t.Fatalf("explicit reissue=%d %s", w.Code, w.Body.String())
	}
	renewed := snapshot()
	if renewed["apply-resource:read"] == after["apply-resource:read"] || !strings.Contains(renewed["apply-resource:read"], f.owner) {
		t.Fatal("explicit renewal lacks authenticated actor")
	}
	for _, key := range []string{"apply-resource:list", "apply-resource:delete_issues", "apply-other:read"} {
		if renewed[key] != after[key] {
			t.Fatalf("unrequested reissue:%s", key)
		}
	}
	if err := f.store.Check(t.Context(), "apply-subject", f.workspace, rights[1]); err != nil {
		t.Fatalf("explicit renewed share inactive:%v", err)
	}
	var payload string
	if err := f.db.QueryRowContext(t.Context(), `SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.access_changed' ORDER BY rowid DESC LIMIT 1`, f.workspace).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"reissued_rights":[{"kind":"project","id":"apply-resource","operation":"read"}]`) || !strings.Contains(payload, `"preset_version":1`) || strings.Contains(payload, "@") {
		t.Fatalf("preset audit incomplete or contains names:%s", payload)
	}
}

func TestPresetApplyAuthenticatedCredentialAndResourceBoundary(t *testing.T) {
	for index, scope := range []any{nil, `[]`, `["workspace:admin"]`, `["workspace:*"]`, `["*"]`, `["agents:write"]`} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			f := newPresetApplyFixture(t)
			credential := "crewship_cli_apply_boundary_synthetic"
			execOrFatal(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes) VALUES('boundary-token',?,'Synthetic apply boundary',?,?)`, f.owner, sha256Hex(credential), scope)
			router, err := NewRouter(f.db, "preset-apply-boundary-synthetic-secret", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			path := "/api/v1/workspaces/" + f.workspace + "/members/" + f.request.MembershipID + "/access/presets/apply"
			do := func(path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+credential)
				r.Header.Set("X-Workspace-ID", f.workspace)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			if index == 5 {
				if w := do(path, applyBody(t, f.preview(t))); w.Code != 403 {
					t.Fatalf("insufficient scope=%d", w.Code)
				}
				policy, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
				if err != nil || len(policy.Rights) != 0 {
					t.Fatalf("denied request wrote policy:%+v %v", policy, err)
				}
				return
			}
			// A foreign resource is opaque, regardless of a valid administration token.
			execOrFatal(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('apply-foreign-ws','Foreign','apply-foreign-ws')`)
			execOrFatal(t, f.db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('apply-foreign-resource','apply-foreign-ws','Foreign','apply-foreign-resource')`)
			p := f.preview(t)
			bad := p
			bad.Selection.Resource.ID = "apply-foreign-resource"
			if w := do(path, applyBody(t, bad)); w.Code != 404 || strings.Contains(w.Body.String(), "apply-foreign-resource") {
				t.Fatalf("foreign resource leaked=%d %s", w.Code, w.Body.String())
			}
			if w := do(strings.Replace(path, "/members/apply-membership/", "/members/missing/", 1), applyBody(t, p)); w.Code != 404 {
				t.Fatalf("foreign membership=%d", w.Code)
			}
			if w := do(path, applyBody(t, p)); w.Code != 200 {
				t.Fatalf("current administrative scope rejected=%d %s", w.Code, w.Body.String())
			}
			policy, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil || policy.Revision <= f.request.Revision || len(policy.Rights) != 2 {
				t.Fatalf("canonical replacement missing: %+v %v", policy, err)
			}
			if err := f.store.Check(t.Context(), "apply-subject", f.workspace, access.Right{Kind: "project", ID: "apply-resource", Operation: "read"}); err != nil {
				t.Fatalf("View read denied: %v", err)
			}
			if err := f.store.Check(t.Context(), "apply-subject", f.workspace, access.Right{Kind: "project", ID: "apply-resource", Operation: "manage"}); err == nil {
				t.Fatal("View grants Manage")
			}
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			choices, err := f.store.PresetSelectionsIn(t.Context(), tx, f.owner, "apply-subject", f.workspace)
			tx.Rollback()
			if err != nil || len(choices) != 1 || choices[0].State != "selected" || choices[0].Level != "view" || choices[0].SelectedBy == nil || *choices[0].SelectedBy != f.owner {
				t.Fatalf("authenticated choice missing: %+v %v", choices, err)
			}
			var events int
			if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM journal_entries WHERE workspace_id=? AND entry_type='member.access_changed'`, f.workspace).Scan(&events); err != nil || events != 1 {
				t.Fatalf("atomic audit missing: %d %v", events, err)
			}
			// Removal/rejoin fences the old membership rather than applying to its replacement.
			execOrFatal(t, f.db, `DELETE FROM workspace_members WHERE id='apply-membership'`)
			execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('apply-rejoined',?,'apply-subject','MEMBER','restricted')`, f.workspace)
			if w := do(path, applyBody(t, p)); w.Code != 404 || strings.Contains(w.Body.String(), "apply-rejoined") {
				t.Fatalf("old incarnation applied or exposed replacement=%d %s", w.Code, w.Body.String())
			}
		})
	}
}
