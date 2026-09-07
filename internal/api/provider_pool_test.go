package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderPoolManagement(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES ('foreign','Foreign','pool-foreign')`); err != nil {
		t.Fatal(err)
	}
	seedCredentialEnc(t, db, ws, user, "pool-account", "Account", "never-return-this-token")
	if _, err := db.ExecContext(t.Context(), `UPDATE credentials SET type='API_KEY', provider='OPENAI' WHERE id='pool-account'`); err != nil {
		t.Fatal(err)
	}
	h := NewProviderPoolHandler(db, quietLogger())
	body := `{"name":"Team","provider":"OPENAI","mode":"api_key","members":[{"credential_id":"pool-account"}]}`
	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER", ""} {
		for _, fn := range []http.HandlerFunc{h.Create, h.List, h.Get} {
			rr := httptest.NewRecorder()
			fn(rr, plRequest(t, "POST", "/", body, user, ws, role))
			if rr.Code != 403 {
				t.Fatalf("role %s: %d %s", role, rr.Code, rr.Body.String())
			}
		}
	}
	rr := httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/", body, user, ws, "ADMIN"))
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create body: %s", rr.Body.String())
	}
	for _, role := range []string{"OWNER", "ADMIN"} {
		for _, fn := range []http.HandlerFunc{h.List, h.Get} {
			req := plRequest(t, "GET", "/", "", user, ws, role)
			req.SetPathValue("poolId", created.ID)
			rr = httptest.NewRecorder()
			fn(rr, req)
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Team") {
				t.Fatalf("read: %d %s", rr.Code, rr.Body.String())
			}
			for _, forbidden := range []string{"never-return-this-token", "encrypted_value", "Generation", "generation"} {
				if strings.Contains(rr.Body.String(), forbidden) {
					t.Fatalf("sensitive response: %s", rr.Body.String())
				}
			}
		}
	}
	rr = httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/", body, user, ws, "OWNER"))
	if rr.Code != 409 {
		t.Fatalf("duplicate: %d %s", rr.Code, rr.Body.String())
	}
	for _, account := range []string{"missing", "pool-account"} {
		rr = httptest.NewRecorder()
		h.Create(rr, plRequest(t, "POST", "/", strings.ReplaceAll(body, "pool-account", account), user, "foreign", "OWNER"))
		if rr.Code != 400 {
			t.Fatalf("foreign/absent member: %d %s", rr.Code, rr.Body.String())
		}
	}
	req := plRequest(t, "GET", "/", "", user, "foreign", "OWNER")
	req.SetPathValue("poolId", created.ID)
	rr = httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Fatalf("foreign get: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "Team") {
		t.Fatalf("foreign list: %d %s", rr.Code, rr.Body.String())
	}
	var turns, grants, audits int
	if err := db.QueryRowContext(t.Context(), `SELECT selection_seq FROM provider_login_pools WHERE id=?`, created.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM credential_bindings`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_logs WHERE action='provider_pool.create' AND entity_id=?`, created.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if turns != 0 || grants != 0 || audits != 1 {
		t.Fatalf("turns=%d grants=%d audits=%d", turns, grants, audits)
	}
}

func TestProviderPoolMutationScope(t *testing.T) {
	t.Parallel()
	const path = "/api/v1/provider-logins/pools"
	if got := scopeForRoute(path); got != "credentials:write" {
		t.Fatalf("scope=%s", got)
	}
	router := &Router{}
	for _, tc := range []struct {
		role, scope string
		want        int
	}{
		{"ADMIN", "credentials:write", 204}, {"OWNER", "credentials:write", 204},
		{"ADMIN", "agents:write", 403}, {"MEMBER", "credentials:write", 403}, {"MANAGER", "credentials:write", 403},
	} {
		t.Run(tc.role+"/"+tc.scope, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := withTokenScopes(plRequest(t, "POST", path, "", "u", "ws", tc.role), tc.scope)
			router.requireRoleScopeMW(roleManage, scopeForRoute(path), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status=%d want=%d", rr.Code, tc.want)
			}
		})
	}
}

func TestProviderPoolPagination(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	u := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, u)
	for i := range 102 {
		id := fmt.Sprintf("pool-%03d", i)
		if _, err := db.ExecContext(t.Context(), `INSERT INTO provider_login_pools(id,workspace_id,name,provider,mode) VALUES (?,?,?,'OPENAI','api_key')`, id, ws, id); err != nil {
			t.Fatal(err)
		}
	}
	h := NewProviderPoolHandler(db, quietLogger())
	var page struct {
		Items      []providerPoolView `json:"items"`
		NextCursor *string            `json:"next_cursor"`
	}
	rr := httptest.NewRecorder()
	h.List(rr, plRequest(t, "GET", "/", "", u, ws, "ADMIN"))
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(page.Items) != 100 || page.NextCursor == nil || *page.NextCursor != "pool-099" {
		t.Fatalf("first page: %s", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.List(rr, plRequest(t, "GET", "/?after="+*page.NextCursor, "", u, ws, "ADMIN"))
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || len(page.Items) != 2 || page.Items[0].ID != "pool-100" || page.NextCursor != nil {
		t.Fatalf("second page: %s", rr.Body.String())
	}
}

func TestProviderPoolInvalidCreate(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	u := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, u)
	seedCredentialEnc(t, db, ws, u, "ordinary", "Ordinary", "fake")
	h := NewProviderPoolHandler(db, quietLogger())
	for _, body := range []string{`{`, `{}`, `{"name":"Empty","provider":"OPENAI","mode":"api_key","members":[]}`, `{"name":"Invalid","provider":"OPENAI","mode":"api_key","members":[{"credential_id":"ordinary"}]}`, `{"name":"Duplicate","provider":"OPENAI","mode":"api_key","members":[{"credential_id":"ordinary"},{"credential_id":"ordinary"}]}`} {
		rr := httptest.NewRecorder()
		h.Create(rr, plRequest(t, "POST", "/", body, u, ws, "OWNER"))
		if rr.Code != 400 {
			t.Fatalf("body=%s status=%d response=%s", body, rr.Code, rr.Body.String())
		}
	}
	for _, body := range []string{`{"value":"not-a-token-endpoint"}`, `{} {}`, `{"name":"` + strings.Repeat("x", 65536) + `"}`} {
		rr := httptest.NewRecorder()
		h.Create(rr, plRequest(t, "POST", "/", body, u, ws, "OWNER"))
		if rr.Code != 400 {
			t.Fatalf("invalid input accepted: %d", rr.Code)
		}
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM provider_login_pools`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid creates persisted %d pools", count)
	}
}

func TestProviderPoolCrossOwnerConsent(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	u := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, u)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO users(id,email) VALUES ('pool-other-owner','pool-other@example.test')`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ id, owner string }{{"first", u}, {"second", "pool-other-owner"}} {
		seedCredentialEnc(t, db, ws, pair.owner, pair.id, pair.id, "fake")
		if _, err := db.ExecContext(t.Context(), `UPDATE credentials SET type='API_KEY',provider='OPENAI' WHERE id=?`, pair.id); err != nil {
			t.Fatal(err)
		}
	}
	h := NewProviderPoolHandler(db, quietLogger())
	body := `{"name":"Mixed","provider":"OPENAI","mode":"api_key","members":[{"credential_id":"first"},{"credential_id":"second"}]}`
	rr := httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/", body, u, ws, "OWNER"))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "explicit consent") {
		t.Fatalf("missing consent: %d %s", rr.Code, rr.Body.String())
	}
	body = strings.Replace(body, `"name":`, `"allow_cross_owner":true,"name":`, 1)
	rr = httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/", body, u, ws, "OWNER"))
	if rr.Code != 201 {
		t.Fatalf("explicit consent: %d %s", rr.Code, rr.Body.String())
	}
}

func TestProviderPoolRouteRegistration(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range r.mutationRoutes {
		if route.Method == "POST" && route.Pattern == "/api/v1/provider-logins/pools" {
			found = true
			if route.Role != roleManage || route.Scope != "credentials:write" {
				t.Fatalf("unsafe route declaration: %+v", route)
			}
		}
	}
	if !found {
		t.Fatal("pool create route is not registered")
	}
	for _, path := range []string{"/api/v1/provider-logins/pools", "/api/v1/provider-logins/pools/missing"} {
		rr := httptest.NewRecorder()
		r.mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, rr.Code)
		}
	}
}
