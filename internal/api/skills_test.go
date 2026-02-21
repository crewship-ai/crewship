package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const validSkillMDForAPI = `---
name: api-test-skill
display_name: API Test Skill
version: 1.0.0
category: CUSTOM
---
# API Test Skill

## Instructions
Test instructions here.`

func TestSkillsImport_ValidContent(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	body := bytes.NewBufferString(`{"content": "` + jsonEscape(validSkillMDForAPI) + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "MANAGER"))
	rr := httptest.NewRecorder()

	handler.Import(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["slug"] != "api-test-skill" {
		t.Errorf("slug = %q, want %q", result["slug"], "api-test-skill")
	}
	if result["skill_id"] == "" || result["skill_id"] == nil {
		t.Error("expected non-empty skill_id in response")
	}
}

func TestSkillsImport_ValidURL(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Serve a mock skill file
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validSkillMDForAPI))
	}))
	defer srv.Close()

	handler := NewSkillHandler(db, logger)
	handler.SkipURLValidation = true // allow localhost for test

	body := bytes.NewBufferString(`{"url": "` + srv.URL + `/SKILL.md"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	handler.Import(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
}

func TestSkillsImport_SSRFBlocked(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)
	// SkipURLValidation is false (default) — SSRF protection active

	tests := []struct {
		name string
		body string
	}{
		{"localhost", `{"url": "https://localhost/SKILL.md"}`},
		{"private_ip", `{"url": "https://10.0.0.1/SKILL.md"}`},
		{"link_local", `{"url": "https://169.254.169.254/latest/meta-data"}`},
		{"http_scheme", `{"url": "http://example.com/SKILL.md"}`},
		{"loopback", `{"url": "https://127.0.0.1/SKILL.md"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(tt.body)
			req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
			req = req.WithContext(withWorkspace(req.Context(), wsID, "MANAGER"))
			rr := httptest.NewRecorder()

			handler.Import(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
		})
	}
}

func TestSkillsImport_ErrorCases(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	tests := []struct {
		name       string
		body       string
		role       string
		wantStatus int
	}{
		{"both_fields_provided", `{"url": "https://example.com/SKILL.md", "content": "` + jsonEscape(validSkillMDForAPI) + `"}`, "MANAGER", http.StatusBadRequest},
		{"missing_both_fields", `{}`, "OWNER", http.StatusBadRequest},
		{"forbidden_role", `{"content": "` + jsonEscape(validSkillMDForAPI) + `"}`, "VIEWER", http.StatusForbidden},
		{"invalid_skillmd", `{"content": "not a valid SKILL.md"}`, "MANAGER", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(tt.body)
			req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
			req = req.WithContext(withWorkspace(req.Context(), wsID, tt.role))
			rr := httptest.NewRecorder()

			handler.Import(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func seedSkillForTest(t *testing.T, db *sql.DB, id, name, category string) {
	t.Helper()
	db.Exec(`INSERT INTO skills (id, name, slug, display_name, version, category, source, verification, downloads, rating_count, pricing_tier, featured, tags, content)
		VALUES (?, ?, ?, ?, '1.0.0', ?, 'CUSTOM', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '# Test')`,
		id, name, name, "Display "+name, category)
}

func TestSkillGet(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedSkillForTest(t, db, "sk1", "test-skill", "CODING")

	handler := NewSkillHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/skills/sk1", nil)
	req.SetPathValue("skillId", "sk1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &result)
	if result["name"] != "test-skill" {
		t.Errorf("name = %v, want test-skill", result["name"])
	}
	if result["content"] != "# Test" {
		t.Errorf("content = %v, want '# Test'", result["content"])
	}
}

func TestSkillGet_NotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/skills/nonexistent", nil)
	req.SetPathValue("skillId", "nonexistent")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestSkillsList_CategoryFilter(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedSkillForTest(t, db, "sk1", "coding-skill", "CODING")
	seedSkillForTest(t, db, "sk2", "research-skill", "RESEARCH")

	handler := NewSkillHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/skills?category=CODING", nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var result []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
}

func TestSkillsList_Search(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedSkillForTest(t, db, "sk1", "alpha-tool", "CODING")
	seedSkillForTest(t, db, "sk2", "beta-widget", "CODING")

	handler := NewSkillHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/skills?search=alpha", nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var result []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
	if result[0]["name"] != "alpha-tool" {
		t.Errorf("name = %v, want alpha-tool", result[0]["name"])
	}
}

// jsonEscape escapes a string for embedding in a JSON string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// b includes surrounding quotes; strip them
	return string(b[1 : len(b)-1])
}
