package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHumanDocumentScopesCanBeMinted(t *testing.T) {
	for _, scopes := range [][]string{{"pages:read"}, {"pages:read", "pages:write"}, {"pages:*"}} {
		t.Run(scopes[len(scopes)-1], func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			seedTestWorkspace(t, db, user)
			body, err := json.Marshal(map[string]any{"name": "document-scope-regression", "scopes": scopes})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: user}))
			response := httptest.NewRecorder()
			NewCLITokenHandler(db, newTestLogger()).Create(response, req)
			if response.Code != 200 {
				t.Fatalf("document scope minting returned %d", response.Code)
			}
			var result struct {
				Scopes []string `json:"scopes"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Scopes) != len(scopes) {
				t.Fatal("minted scope set changed")
			}
			for i, scope := range scopes {
				if result.Scopes[i] != scope {
					t.Fatal("minted scope set changed")
				}
			}
		})
	}
}

func TestHumanDocumentMutationRoutesUseDocumentScope(t *testing.T) {
	router, err := NewRouter(setupTestDB(t), "synthetic-document-scope-router-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	for _, path := range []string{"/api/v1/pages/{slug}/document/initialize", "/api/v1/pages/{slug}/document/patch"} {
		found := false
		for _, route := range router.mutationRoutes {
			if route.Pattern == path {
				found = true
				if route.Scope != "pages:write" {
					t.Errorf("%s uses %q, want pages:write", path, route.Scope)
				}
			}
		}
		if !found {
			t.Errorf("missing registered mutation %s", path)
		}
	}
	if scopeForRoute("/api/v1/pages") != "workspace:admin" {
		t.Fatal("legacy Page creation scope changed")
	}
}
