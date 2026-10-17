package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestPresetReadRoutesAuthenticatedContract(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('preset-user','preset-user@test.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('preset-member',?,'preset-user','MEMBER','restricted')`, ws)
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('preset-project',?,'Project','preset-project')`, ws)
	p, err := (access.Store{DB: db}).Policy(t.Context(), owner, "preset-user", ws)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "preset-read-routes-synthetic-secret-32chars"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), owner, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(owner, session.ID, owner, "owner@test.invalid")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	body, err := json.Marshal(map[string]any{"membership_id": p.ID, "revision": p.Revision, "resource": map[string]string{"kind": "project", "id": "preset-project"}, "preset_id": "project-levels", "preset_version": 1, "level": "manage", "reissue": []access.Right{}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/workspaces/" + ws
	previewPath := base + "/members/preset-member/access/presets/preview"
	do := func(credential, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-Workspace-ID", ws)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := do(token, "POST", previewPath, string(body))
	if w.Code != 200 {
		t.Fatalf("authenticated preset preview returned %d, want200: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("administrative preview cacheable")
	}
	for i, scopes := range []any{nil, `["workspace:admin"]`, `["workspace:*"]`, `["*"]`, `["agents:write"]`, `[]`} {
		credential := "crewship_cli_preset-scope-" + strconv.Itoa(i)
		execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes) VALUES(?,?,?, ?,?)`, "preset-token-"+strconv.Itoa(i), owner, "Synthetic scope contract", sha256Hex(credential), scopes)
		for _, tc := range []struct{ method, path, body string }{{"GET", base + "/members/preset-member/access", ""}, {"POST", previewPath, string(body)}} {
			w := do(credential, tc.method, tc.path, tc.body)
			want := 200
			// The existing token parser deliberately treats [] like legacy NULL
			// scopes. Preserve that current GET-policy contract in these reads.
			if i == 4 {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("scope%v %s got%d want%d: %s", scopes, tc.path, w.Code, want, w.Body.String())
			}
		}
	}
	for _, bad := range []string{
		string(body) + ` {}`, strings.Replace(string(body), `"level":"manage"`, `"level":"publish"`, 1),
		strings.Replace(string(body), `"preset_version":1`, `"preset_version":2`, 1),
		strings.Replace(string(body), `"resource":{`, `"resource":{"actor":"forged",`, 1),
		strings.TrimSuffix(string(body), "}") + `,"actor":"forged"}`,
		`{"membership_id":"preset-member","revision":1,"padding":"` + strings.Repeat("x", 256<<10) + `"}`,
	} {
		if w := do(token, "POST", previewPath, bad); w.Code != 400 {
			t.Fatalf("invalid input got%d: %s", w.Code, w.Body.String())
		}
	}
	var preview access.PresetPreview
	w = do(token, "POST", previewPath, string(body))
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || len(preview.AfterRights) != 4 || preview.Digest == "" {
		t.Fatalf("incomplete preview: %s %v", w.Body.String(), err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil || string(wire["before"]) != "[]" {
		t.Fatalf("first-share before array contract:%s %v", w.Body.String(), err)
	}
	after, err := (access.Store{DB: db}).Policy(t.Context(), owner, "preset-user", ws)
	if err != nil || !reflect.DeepEqual(after, p) {
		t.Fatalf("HTTP preview wrote policy: %v", err)
	}
	for _, foreign := range []string{
		base + "/members/foreign-member/access/presets/preview",
		"/api/v1/workspaces/foreign/members/preset-member/access/presets/preview?workspace_id=" + ws,
	} {
		if w := do(token, "POST", foreign, string(body)); w.Code != 404 {
			t.Fatalf("foreign target got%d", w.Code)
		}
	}
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('preset-foreign','Foreign','preset-foreign')`)
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('foreign-project','preset-foreign','Foreign','foreign-project')`)
	foreignBody := strings.Replace(string(body), `"id":"preset-project"`, `"id":"foreign-project"`, 1)
	if w := do(token, "POST", previewPath, foreignBody); w.Code != 404 || strings.Contains(w.Body.String(), "foreign-project") {
		t.Fatalf("foreign resource leaked: %d %s", w.Code, w.Body.String())
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_mode='trusted' WHERE id='preset-member'`)
	current, err := (access.Store{DB: db}).Membership(t.Context(), "preset-user", ws)
	if err != nil {
		t.Fatal(err)
	}
	trusted := strings.Replace(string(body), `"revision":`+strconv.FormatInt(p.Revision, 10), `"revision":`+strconv.FormatInt(current.Revision, 10), 1)
	if w := do(token, "POST", previewPath, trusted); w.Code != 400 {
		t.Fatalf("trusted target accepted:%d", w.Code)
	}
	if w := do(token, "POST", previewPath, string(body)); w.Code != 409 {
		t.Fatalf("stale revision accepted:%d", w.Code)
	}
	execOrFatal(t, db, `DELETE FROM workspace_members WHERE id='preset-member'`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('preset-rejoined',?,'preset-user','MEMBER','restricted')`, ws)
	if w := do(token, "POST", previewPath, string(body)); w.Code != 404 || strings.Contains(w.Body.String(), "preset-rejoined") {
		t.Fatalf("old incarnation exposed replacement:%d %s", w.Code, w.Body.String())
	}
	// Current caller state is checked again, regardless of a still-valid JWT.
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, ws)
	if w := do(token, "GET", base+"/access/presets", ""); w.Code != 404 {
		t.Fatalf("demoted caller catalog:%d", w.Code)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=? AND workspace_id=?`, owner, ws)
	for _, tc := range []struct{ method, path string }{{"GET", base + "/access/presets"}, {"POST", previewPath}} {
		if w := do(token, tc.method, tc.path, string(body)); w.Code == 200 {
			t.Fatal("restricted caller escaped route ceiling")
		}
	}
}
