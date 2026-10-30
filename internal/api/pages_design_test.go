package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func designFixture(t *testing.T) (*PageHandler, string, string, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	h, _, _, ws, owner := newPagesFixture(t)
	h.db = artifactProjectFullDurability(t, h.db)
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	tokens := map[string]string{}
	call := func(user, operation, body string) *httptest.ResponseRecorder {
		token := tokens[user]
		if token == "" {
			token = mintTokenFor(t, h.db, user, "design-test-"+user)
			tokens[user] = token
		}
		r := httptest.NewRequest("POST", "/api/v1/pages/board/design", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "MEMBER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "board")
		w := httptest.NewRecorder()
		switch operation {
		case "create":
			h.CreateDesign(w, r)
		case "comments":
			h.GetDesignComments(w, r)
		case "comment":
			h.CreateDesignComment(w, r)
		case "share":
			h.ShareDesign(w, r)
		case "agent":
			h.ConnectDesignAgent(w, r)
		case "get":
			h.GetDesign(w, r)
		case "apply":
			h.ApplyDesign(w, r)
		case "snapshot":
			h.SnapshotDesign(w, r)
		case "restore":
			h.RestoreDesign(w, r)
		case "versions":
			h.DesignVersions(w, r)
		}
		return w
	}
	return h, ws, owner, call
}
func TestDesignOfflineAgentShapeAndVersionRestore(t *testing.T) {
	h, _, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Design board"}`); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	op := `{"operations":[{"op":"upsert","node":{"id":"cart","type":"frame","name":"Košík","x":10,"y":20,"width":320,"height":640,"fill":"#ffffff"}}]}`
	if w := call(owner, "apply", op); w.Code != 200 {
		t.Fatalf("offline apply %d %s", w.Code, w.Body)
	}
	if w := call(owner, "snapshot", `{"message":"Cart"}`); w.Code != 200 {
		t.Fatalf("snapshot %d %s", w.Code, w.Body)
	}
	if w := call(owner, "restore", `{"version":1}`); w.Code != 200 {
		t.Fatalf("restore %d %s", w.Code, w.Body)
	}
	w := call(owner, "get", "")
	if w.Code != 200 {
		t.Fatalf("get %d %s", w.Code, w.Body)
	}
	var value struct {
		Nodes   []json.RawMessage `json:"nodes"`
		Version int               `json:"version"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &value)
	if len(value.Nodes) != 0 || value.Version < 3 {
		t.Fatalf("restore %s", w.Body)
	}
	var n int
	if e := h.db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&n); e != nil || n < 3 {
		t.Fatalf("ordinary artifact history %d %v", n, e)
	}
}
func TestDesignReadGrantNeverWritesAndRevocationIsLive(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Board"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "share", `{"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	user := "design-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'design-reader@example.test','Reader')`, user)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
	var page string
	_ = h.db.QueryRow(`SELECT id FROM pages WHERE slug='board'`).Scan(&page)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, user, owner)
	if w := call(user, "get", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"can_edit":false`) {
		t.Fatalf("reader %d %s", w.Code, w.Body)
	}
	for _, op := range []string{"apply", "snapshot", "restore"} {
		if w := call(user, op, `{"operations":[],"version":1}`); w.Code != 403 && w.Code != 400 {
			t.Errorf("reader %s %d %s", op, w.Code, w.Body)
		}
	}
	mustExec(t, h.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_id=?`, page, user)
	if w := call(user, "snapshot", `{}`); w.Code != 200 {
		t.Fatalf("editor %d %s", w.Code, w.Body)
	}
	mustExec(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_id=?`, page, user)
	if w := call(user, "snapshot", `{}`); w.Code != 403 {
		t.Fatalf("downgrade not immediate %d %s", w.Code, w.Body)
	}
	mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
	if w := call(user, "get", ""); w.Code != 404 {
		t.Fatalf("read revoke %d %s", w.Code, w.Body)
	}
}

func TestDesignReaderHistoryAndCommentsStayAtSharedPin(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	for _, step := range []struct{ op, body string }{
		{"create", `{"slug":"board","name":"Board"}`},
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"frame","type":"frame","name":"Frame","x":0,"y":0,"width":300,"height":600}}],"message":"shared-version"}`},
		{"share", `{"version":2}`},
		{"comment", `{"version":2,"path":"design.json","selector":"frame","quote":"Frame","text":"Shared feedback","send_to_agent":false}`},
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"frame","fill":"#ff0000"}}],"message":"private-version-message"}`},
		{"comment", `{"version":3,"path":"design.json","selector":"frame","quote":"Frame","text":"private-feedback","send_to_agent":false}`},
	} {
		w := call(owner, step.op, step.body)
		if w.Code < 200 || w.Code > 201 {
			t.Fatalf("%s %d %s", step.op, w.Code, w.Body)
		}
	}
	user := "design-shared-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'design-shared-reader@example.test','Shared Reader')`, user)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
	var page string
	_ = h.db.QueryRow(`SELECT id FROM pages WHERE slug='board'`).Scan(&page)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, user, owner)
	for _, op := range []string{"versions", "comments"} {
		w := call(user, op, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
			t.Fatalf("reader %s leaked history %d %s", op, w.Code, w.Body)
		}
	}
	if w := call(user, "get", ""); w.Code != 200 || strings.Contains(w.Body.String(), "#ff0000") || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("readonly canvas escaped its shared snapshot: %d %s", w.Code, w.Body)
	}
}

func TestDesignFailedVersionPublicationLeavesLiveStateUntouched(t *testing.T) {
	h, _, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Board"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	mustExec(t, h.db, `CREATE TRIGGER design_simulated_version_fault BEFORE INSERT ON artifact_versions WHEN NEW.version>1 BEGIN SELECT RAISE(ABORT,'synthetic history failure');END`)
	body := `{"operations":[{"op":"upsert","node":{"id":"must-not-commit","type":"frame","x":0,"y":0,"width":300,"height":600}}]}`
	if w := call(owner, "apply", body); w.Code != 500 {
		t.Fatalf("synthetic version failure %d %s", w.Code, w.Body)
	}
	w := call(owner, "get", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "must-not-commit") {
		t.Fatalf("failed version leaked partial live mutation: %d %s", w.Code, w.Body)
	}
	var revision int64
	if e := h.db.QueryRow(`SELECT revision FROM artifact_design_documents`).Scan(&revision); e != nil || revision != 0 {
		t.Fatalf("failed version committed mutable revision %d %v", revision, e)
	}
}
func TestDesignCredentialPreflightDoesNotRevealPageExistence(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Board"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	token := mintIngressScopedToken(t, h.db, owner, "design-prescope", "agents:write")
	var first string
	for _, slug := range []string{"board", "absent"} {
		r := httptest.NewRequest("GET", "/api/v1/pages/"+slug+"/design", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: owner}), ws, "OWNER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		h.GetDesign(w, r)
		if w.Code != 403 {
			t.Fatalf("under-scoped %s %d %s", slug, w.Code, w.Body)
		}
		if first == "" {
			first = w.Body.String()
		} else if first != w.Body.String() {
			t.Fatal("under-scoped token can distinguish existing Design")
		}
	}
}
