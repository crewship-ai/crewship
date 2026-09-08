package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderPoolLifecycleAPI(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCredentialEnc(t, db, ws, user, "account", "Account", "test-only")
	if _, err := db.ExecContext(t.Context(), `UPDATE credentials SET type='API_KEY',provider='OPENAI' WHERE id='account'`); err != nil {
		t.Fatal(err)
	}
	h := NewProviderPoolHandler(db, quietLogger())
	rr := httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/", `{"name":"Team","provider":"OPENAI","mode":"api_key","members":[{"credential_id":"account"}]}`, user, ws, "ADMIN"))
	var p providerPoolView
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil || rr.Code != 201 || p.Revision != 1 || rr.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	update := `{"name":"Updated","members":[{"credential_id":"account","priority":5}]}`
	for _, tc := range []struct {
		name, role, tenant, tag, body string
		handler                       http.HandlerFunc
		status                        int
	}{
		{"member update", "MEMBER", ws, `"1"`, update, h.Update, 403},
		{"manager delete", "MANAGER", ws, `"1"`, "", h.Delete, 403},
		{"foreign update", "ADMIN", "foreign", `"1"`, update, h.Update, 404},
		{"foreign delete", "ADMIN", "foreign", `"1"`, "", h.Delete, 404},
		{"missing precondition", "ADMIN", ws, "", update, h.Update, 428},
		{"wildcard", "ADMIN", ws, "*", update, h.Update, 400},
		{"weak tag", "ADMIN", ws, `W/"1"`, update, h.Update, 400},
		{"unknown field", "ADMIN", ws, `"1"`, `{"provider":"GOOGLE"}`, h.Update, 400},
		{"update", "ADMIN", ws, `"1"`, update, h.Update, 204},
		{"stale update", "OWNER", ws, `"1"`, update, h.Update, 412},
		{"stale delete", "OWNER", ws, `"1"`, "", h.Delete, 412},
		{"delete", "OWNER", ws, `"2"`, "", h.Delete, 204},
		{"repeat delete", "OWNER", ws, `"2"`, "", h.Delete, 404},
		{"get retired", "OWNER", ws, "", "", h.Get, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := plRequest(t, "PUT", "/", tc.body, user, tc.tenant, tc.role)
			req.SetPathValue("poolId", p.ID)
			req.Header.Set("If-Match", tc.tag)
			out := httptest.NewRecorder()
			tc.handler(out, req)
			if out.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", out.Code, tc.status, out.Body.String())
			}
			if tc.name == "update" && out.Header().Get("ETag") != `"2"` {
				t.Fatal("update ETag missing")
			}
		})
	}
	out := httptest.NewRecorder()
	h.List(out, plRequest(t, "GET", "/", "", user, ws, "OWNER"))
	var page struct {
		Items []providerPoolView `json:"items"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil || len(page.Items) != 0 {
		t.Fatalf("retired list: %s", out.Body.String())
	}
}

func TestProviderPoolLifecycleRoutes(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			found := false
			for _, route := range r.mutationRoutes {
				if route.Method == method && route.Pattern == "/api/v1/provider-logins/pools/{poolId}" {
					found = true
					if route.Role != roleManage || route.Scope != "credentials:write" {
						t.Fatalf("unsafe route: %+v", route)
					}
				}
			}
			if !found {
				t.Fatal("mutation route missing")
			}
			rr := httptest.NewRecorder()
			r.mux.ServeHTTP(rr, httptest.NewRequest(method, "/api/v1/provider-logins/pools/pool", nil))
			if rr.Code != 401 {
				t.Fatalf("unauthenticated mutation: %d", rr.Code)
			}
		})
	}
}
