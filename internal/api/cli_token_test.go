package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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
	gotUserID, gotEmail, _, err := ValidateCLIToken(context.Background(), db, created.Token, ValidateAuditContext{})
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

	_, _, _, err := ValidateCLIToken(context.Background(), db, "crewship_cli_nonexistent_token_0000", ValidateAuditContext{})
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

	// Validate should fail after revoke. Post-Patch-J the validator
	// collapses all failure reasons (revoked / expired / not-found /
	// tier-mismatch) into a single "invalid CLI token" so a caller
	// cannot oracle which condition fired. Specifics go to the logger
	// for operator audit.
	_, _, _, err := ValidateCLIToken(context.Background(), db, created.Token, ValidateAuditContext{})
	if err == nil {
		t.Error("expected error for revoked token")
	}
	if err != nil && !strings.Contains(err.Error(), "invalid CLI token") {
		t.Errorf("error = %q, want generic 'invalid CLI token'", err.Error())
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

// ---- New / extended cases ----

func TestCLITokenValidate_HandlerOK(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("POST", "/api/v1/auth/cli-tokens/validate", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Validate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestCLITokenValidate_HandlerUnauthorized(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("POST", "/api/v1/auth/cli-tokens/validate", nil)
	rr := httptest.NewRecorder()
	h.Validate(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCLITokenList_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/auth/cli-tokens", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCLITokenList_OnlyOwnTokens(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	userA := seedTestUser(t, db)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('user-b', 'b@b.com', 'B')`); err != nil {
		t.Fatalf("seed userB: %v", err)
	}

	// userA has 1 token, userB has 1 token
	for i, uid := range []string{userA, "user-b"} {
		body, _ := json.Marshal(map[string]string{"name": "tok-" + string(rune('A'+i))})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: uid}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
	}

	// List as userA — must see only their own
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userA}))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Errorf("user A should see only own token, got %d", len(resp.Data))
	}
}

func TestCLITokenRevoke_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/x", nil)
	req.SetPathValue("tokenId", "x")
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCLITokenRevoke_OtherUserCannotRevoke(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	userA := seedTestUser(t, db)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('user-b', 'b@b.com', 'B')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// userA creates a token
	body, _ := json.Marshal(map[string]string{"name": "userA-tok"})
	createReq := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	createReq = createReq.WithContext(withUser(createReq.Context(), &AuthUser{ID: userA}))
	createRR := httptest.NewRecorder()
	h.Create(createRR, createReq)
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(createRR.Body.Bytes(), &created)

	// userB tries to revoke userA's token — should 404 (not their token)
	req := httptest.NewRequest("DELETE", "/api/v1/auth/cli-tokens/"+created.ID, nil)
	req.SetPathValue("tokenId", created.ID)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "user-b"}))
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-user revoke status = %d, want 404 (cannot reveal existence)", rr.Code)
	}
}

// TestCLITokenCreate_AdminTier_RequiresOwnerRole pins Patch J: a
// caller who isn't OWNER of any workspace cannot mint an ADMIN-tier
// token regardless of whether the HMAC key is configured.
func TestCLITokenCreate_AdminTier_RequiresOwnerRole(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 32 bytes hex
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // seedTestUser does NOT make them OWNER of anything
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	body, _ := json.Marshal(map[string]any{"name": "ops", "tier": "ADMIN"})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("non-OWNER ADMIN issuance status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCLITokenCreate_AdminTier_RequiresHMACKey pins Patch J: an OWNER
// who tries to mint an ADMIN token gets 503 when the server-side HMAC
// key isn't configured (so a misdeployed instance returns a clear
// "fix your env" instead of silently falling back to SHA-256 and
// collapsing the two tiers into one).
func TestCLITokenCreate_AdminTier_RequiresHMACKey(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY", "") // explicitly unset
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID) // user becomes OWNER via seedTestWorkspace
	_ = wsID
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	body, _ := json.Marshal(map[string]any{"name": "ops", "tier": "ADMIN"})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("missing HMAC key status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "CREWSHIP_ADMIN_TOKEN_HMAC_KEY") {
		t.Errorf("error body should name the env var; got %s", rr.Body.String())
	}
}

// TestCLITokenCreate_AdminTier_HappyPath issues an ADMIN token end to
// end: OWNER user, HMAC key configured, default expiry (24h),
// validates back through ValidateCLIToken.
func TestCLITokenCreate_AdminTier_HappyPath(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	body, _ := json.Marshal(map[string]any{"name": "ops", "tier": "ADMIN"})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "u@e.com", Name: "U"}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Token     string `json:"token"`
		Tier      string `json:"tier"`
		ExpiresAt string `json:"expires_at"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if !strings.HasPrefix(resp.Token, "crewship_admin_") {
		t.Errorf("expected crewship_admin_ prefix, got %q", resp.Token)
	}
	if resp.Tier != "ADMIN" {
		t.Errorf("tier = %q, want ADMIN", resp.Tier)
	}
	if resp.ExpiresAt == "" {
		t.Errorf("ADMIN response must include expires_at")
	}

	uid, email, _, vErr := ValidateCLIToken(context.Background(), db, resp.Token, ValidateAuditContext{})
	if vErr != nil {
		t.Fatalf("ValidateCLIToken: %v", vErr)
	}
	if uid != userID {
		t.Errorf("validated user_id = %q, want %q", uid, userID)
	}
	if email == "" {
		t.Errorf("validated email must not be empty")
	}
}

// TestCLITokenCreate_AdminTier_CapsExpiryAt7Days proves the handler
// clamps a hostile "expires_in_seconds: 99999999" down to the 7-day
// ceiling instead of issuing a 3-year-lived ADMIN token.
func TestCLITokenCreate_AdminTier_CapsExpiryAt7Days(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	body, _ := json.Marshal(map[string]any{
		"name":               "ops",
		"tier":               "ADMIN",
		"expires_in_seconds": 99999999, // 3+ years
	})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp struct {
		ExpiresAt string `json:"expires_at"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)

	expiresAt, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
	maxAllowed := time.Now().Add(7*24*time.Hour + time.Minute) // 1 min jitter for test wall-clock
	if expiresAt.After(maxAllowed) {
		t.Errorf("expires_at = %s exceeds 7-day cap (max %s)", expiresAt, maxAllowed)
	}
}

// TestValidateCLIToken_ExpiredTokenRefused — STANDARD or ADMIN, an
// expired token comes back from the validator as the same generic
// "invalid CLI token" the caller would see for revoked / not-found.
func TestValidateCLIToken_ExpiredTokenRefused(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	// 60-second TTL (minimum allowed) — we'll then manually backdate
	// expires_at to put it in the past.
	body, _ := json.Marshal(map[string]any{
		"name":               "soon-to-expire",
		"tier":               "ADMIN",
		"expires_in_seconds": 60,
	})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)

	// Backdate expires_at into the past.
	pastTS := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE cli_tokens SET expires_at = ? WHERE id = ?`, pastTS, resp.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	_, _, _, vErr := ValidateCLIToken(context.Background(), db, resp.Token, ValidateAuditContext{})
	if vErr == nil {
		t.Errorf("expected expired token to be rejected")
	}
	if vErr != nil && !strings.Contains(vErr.Error(), "invalid CLI token") {
		t.Errorf("expired token error = %q, want generic 'invalid CLI token'", vErr.Error())
	}
}

// TestCLITokenCreate_Scopes_Validated pins Patch M2's scope path:
// unknown scope → 400; over-privileged scope (MEMBER asks for
// agents:write) → 403 naming the scope; OWNER-issued scoped token
// → 200 with the scopes echoed back in the response.
func TestCLITokenCreate_Scopes_Validated(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	t.Run("unknown_scope_rejected_400", func(t *testing.T) {
		db := setupTestDB(t)
		h := NewCLITokenHandler(db, logger)
		userID := seedTestUser(t, db)
		seedTestWorkspace(t, db, userID)
		body, _ := json.Marshal(map[string]any{
			"name":   "t",
			"scopes": []string{"agents:flyToTheMoon"},
		})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for unknown scope; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "agents:flyToTheMoon") {
			t.Errorf("error must name the offending scope; got %s", rr.Body.String())
		}
	})

	t.Run("over_privileged_scope_rejected_403_with_named_scope", func(t *testing.T) {
		// Seed a user as MEMBER (not MANAGER), then ask for agents:write
		// which requires MANAGER+. The role-vs-scope gate fires.
		db := setupTestDB(t)
		h := NewCLITokenHandler(db, logger)
		const u = "u-member"
		execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'm@x', 'M')`, u)
		execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('w-mem', 'W', 'w-mem')`)
		execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-mem', 'w-mem', ?, 'MEMBER')`, u)

		body, _ := json.Marshal(map[string]any{
			"name":   "t",
			"scopes": []string{"agents:write"},
		})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: u}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 for over-privileged scope", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "agents:write") {
			t.Errorf("403 body must name the offending scope; got %s", rr.Body.String())
		}
	})

	t.Run("owner_can_issue_scoped_token", func(t *testing.T) {
		db := setupTestDB(t)
		h := NewCLITokenHandler(db, logger)
		userID := seedTestUser(t, db)
		seedTestWorkspace(t, db, userID) // makes userID OWNER
		body, _ := json.Marshal(map[string]any{
			"name":   "ci-bot",
			"scopes": []string{"agents:read", "agents:run"},
		})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var resp struct {
			Scopes []string `json:"scopes"`
		}
		json.Unmarshal(rr.Body.Bytes(), &resp)
		if len(resp.Scopes) != 2 {
			t.Errorf("expected 2 scopes echoed back, got %v", resp.Scopes)
		}
	})

	t.Run("unscoped_token_path_unchanged", func(t *testing.T) {
		db := setupTestDB(t)
		h := NewCLITokenHandler(db, logger)
		userID := seedTestUser(t, db)
		// Deliberately no seedTestWorkspace — pre-M2 the test "create a
		// basic token" never required workspace membership. M2 must
		// not regress that path; unscoped tokens skip the role lookup.
		body, _ := json.Marshal(map[string]any{"name": "basic"})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("unscoped path broke: status = %d, body=%s", rr.Code, rr.Body.String())
		}
	})
}

// TestIsCLIToken_AcceptsBothTiers proves the prefix matcher returns
// true for either tier so the middleware token-dispatch keeps working
// without per-tier branches at the AuthMiddleware layer.
func TestIsCLIToken_AcceptsBothTiers(t *testing.T) {
	if !IsCLIToken("crewship_cli_abc") {
		t.Error("standard prefix should match")
	}
	if !IsCLIToken("crewship_admin_abc") {
		t.Error("admin prefix should match")
	}
	if IsCLIToken("not_a_token") {
		t.Error("non-token must not match")
	}
}

func TestCLITokenCreate_TokenIsRandomAndUnique(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCLITokenHandler(db, logger)

	tokens := map[string]bool{}
	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(map[string]string{"name": "n"})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		var resp struct {
			Token string `json:"token"`
		}
		json.Unmarshal(rr.Body.Bytes(), &resp)
		if tokens[resp.Token] {
			t.Errorf("duplicate token generated: %q", resp.Token)
		}
		tokens[resp.Token] = true
		// Post-Patch-J: 32 random bytes hex-encoded = 64 chars.
		// 13 (prefix "crewship_cli_") + 64 (hex) = 77.
		if len(resp.Token) != 77 {
			t.Errorf("token length = %d, want 77 (prefix 13 + 64-char hex of 32-byte random)", len(resp.Token))
		}
	}
}
