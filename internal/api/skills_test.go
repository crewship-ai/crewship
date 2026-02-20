package api

import (
	"bytes"
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
		url  string
	}{
		{"localhost", `{"url": "https://localhost/SKILL.md"}`},
		{"private_ip", `{"url": "https://10.0.0.1/SKILL.md"}`},
		{"link_local", `{"url": "https://169.254.169.254/latest/meta-data"}`},
		{"http_scheme", `{"url": "http://example.com/SKILL.md"}`},
		{"loopback", `{"url": "https://127.0.0.1/SKILL.md"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(tt.url)
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

func TestSkillsImport_MissingBothFields(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	handler.Import(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestSkillsImport_ForbiddenRole(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	body := bytes.NewBufferString(`{"content": "` + jsonEscape(validSkillMDForAPI) + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "VIEWER"))
	rr := httptest.NewRecorder()

	handler.Import(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func TestSkillsImport_InvalidSKILLMD(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewSkillHandler(db, logger)

	body := bytes.NewBufferString(`{"content": "not a valid SKILL.md"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/import", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "MANAGER"))
	rr := httptest.NewRecorder()

	handler.Import(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

// jsonEscape escapes a string for embedding in a JSON string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// b includes surrounding quotes; strip them
	return string(b[1 : len(b)-1])
}
