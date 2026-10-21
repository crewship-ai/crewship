package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillImportRouteHonorsLiveCapability(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	token := seedRoleMemberToken(t, db, ws, "skill-viewer", "VIEWER", "skillimportcapability000000000")
	if _, err := db.Exec(`UPDATE workspace_members SET access_mode='trusted',capabilities='["chat","skill.create"]' WHERE user_id='skill-viewer'`); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, "skill-import-regression-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := func(content string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"content": content})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws+"/skills/import", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request(validSkillMDForAPI)
	if response.Code != http.StatusCreated {
		t.Fatalf("granted VIEWER import = %d, want201: %s", response.Code, response.Body.String())
	}
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities='["chat"]' WHERE user_id='skill-viewer'`); err != nil {
		t.Fatal(err)
	}
	InvalidateCapabilityCache(ws, "skill-viewer")
	response = request(strings.ReplaceAll(validSkillMDForAPI, "api-test-skill", "revoked-skill"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked VIEWER import = %d, want403: %s", response.Code, response.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE slug='revoked-skill'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("denied import created a skill")
	}
}
