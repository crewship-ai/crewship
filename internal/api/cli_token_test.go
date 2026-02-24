package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCLITokenCreate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	body, _ := json.Marshal(map[string]string{"name": "my-cli-token"})
	req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()

	h.Create(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result struct {
		Token     string `json:"token"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !strings.HasPrefix(result.Token, "crewship_cli_") {
		t.Errorf("token prefix = %q, want crewship_cli_*", result.Token[:10])
	}
	if result.Name != "my-cli-token" {
		t.Errorf("name = %q, want my-cli-token", result.Name)
	}
	if result.ID == "" {
		t.Error("id should not be empty")
	}
}

func TestCLITokenCreateDefaultName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader("{}"))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()

	h.Create(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	var result struct {
		Name string `json:"name"`
	}
	json.Unmarshal(rr.Body.Bytes(), &result)
	if result.Name != "CLI token" {
		t.Errorf("name = %q, want 'CLI token'", result.Name)
	}
}

func TestCLITokenValidate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	// Create a token first
	body, _ := json.Marshal(map[string]string{"name": "test-validate"})
	createReq := httptest.NewRequest("POST", "/api/v1/auth/cli-token", bytes.NewReader(body))
	createReq = createReq.WithContext(withUser(createReq.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	createRR := httptest.NewRecorder()
	h.Create(createRR, createReq)

	var created struct {
		Token string `json:"token"`
	}
	json.Unmarshal(createRR.Body.Bytes(), &created)

	// Validate via DB function
	gotUserID, gotEmail, _, err := ValidateCLIToken(db, created.Token)
	if err != nil {
		t.Fatalf("ValidateCLIToken() error: %v", err)
	}
	if gotUserID != userID {
		t.Errorf("userID = %q, want %q", gotUserID, userID)
	}
	if gotEmail != "test@example.com" {
		t.Errorf("email = %q, want test@example.com", gotEmail)
	}
}

func TestCLITokenValidateInvalid(t *testing.T) {
	db := setupTestDB(t)

	_, _, _, err := ValidateCLIToken(db, "crewship_cli_nonexistent_token_0000")
	if err == nil {
		t.Error("expected error for invalid token")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error = %q, want to contain 'invalid'", err.Error())
	}
}

func TestCLITokenRevoke(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	// Create
	body, _ := json.Marshal(map[string]string{"name": "revoke-me"})
	createReq := httptest.NewRequest("POST", "/api/v1/auth/cli-token", bytes.NewReader(body))
	createReq = createReq.WithContext(withUser(createReq.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	createRR := httptest.NewRecorder()
	h.Create(createRR, createReq)

	var created struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	json.Unmarshal(createRR.Body.Bytes(), &created)

	// Revoke
	revokeReq := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/"+created.ID, nil)
	revokeReq.SetPathValue("tokenId", created.ID)
	revokeReq = revokeReq.WithContext(withUser(revokeReq.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	revokeRR := httptest.NewRecorder()
	h.Revoke(revokeRR, revokeReq)

	if revokeRR.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, body = %s", revokeRR.Code, revokeRR.Body.String())
	}

	// Validate should fail after revoke
	_, _, _, err := ValidateCLIToken(db, created.Token)
	if err == nil {
		t.Error("expected error for revoked token")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error = %q, want to contain 'revoked'", err.Error())
	}
}

func TestCLITokenList(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	// Create two tokens
	for _, name := range []string{"token-1", "token-2"} {
		body, _ := json.Marshal(map[string]string{"name": name})
		req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
	}

	// List
	listReq := httptest.NewRequest("GET", "/api/v1/auth/cli-tokens", nil)
	listReq = listReq.WithContext(withUser(listReq.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)

	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRR.Code)
	}

	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	json.Unmarshal(listRR.Body.Bytes(), &result)

	if len(result.Data) != 2 {
		t.Errorf("got %d tokens, want 2", len(result.Data))
	}
}

func TestCLITokenRevokeNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/nonexistent", nil)
	req.SetPathValue("tokenId", "nonexistent")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("revoke nonexistent status = %d, want 404", rr.Code)
	}
}

func TestIsCLIToken(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"crewship_cli_abc123", true},
		{"crewship_cli_", true},
		{"bearer_abc123", false},
		{"", false},
		{"crewship_cliabc", false},
	}

	for _, tt := range tests {
		got := IsCLIToken(tt.token)
		if got != tt.want {
			t.Errorf("IsCLIToken(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestCLITokenCreateUnauthorized(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader("{}"))
	// No user in context
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}
