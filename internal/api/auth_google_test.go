package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/crewship-ai/crewship/internal/auth"
)

func newTestGoogleHandler(t *testing.T, db *sql.DB) *GoogleAuthHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	validator, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!", "")
	require.NoError(t, err)
	return NewGoogleAuthHandler(db, logger, validator, "test-client-id", "test-secret", "http://localhost:8080")
}

func TestGoogleOAuth_StateIsStoredAndConsumed(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)

	// Insert a state manually (simulating what Redirect does)
	state := "test-state-value-12345"
	_, err := db.Exec(
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, '', '', '/dashboard')`,
		state)
	require.NoError(t, err)

	// Verify state exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM oauth_states WHERE state = ?", state).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Callback with invalid state should fail
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?state=invalid-state&code=test-code", nil)
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Valid state should be consumed (but token exchange will fail — that's expected)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?state="+state+"&code=test-code", nil)
	rec = httptest.NewRecorder()
	h.Callback(rec, req)
	// Exchange will fail (no real Google), but state should be consumed
	// The handler returns 400 on exchange failure, not 200

	// State should be consumed (deleted) after callback attempt
	err = db.QueryRow("SELECT COUNT(*) FROM oauth_states WHERE state = ?", state).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "state should be single-use and deleted after consumption")
}

func TestGoogleOAuth_ExpiredStateRejected(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)

	// Insert an expired state (20 minutes ago)
	state := "expired-state-value"
	createdAt := time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, created_at) VALUES (?, '', '', '/', ?)`,
		state, createdAt)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?state="+state+"&code=test-code", nil)
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGoogleOAuth_MissingStateOrCode(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)

	tests := []struct {
		name  string
		query string
	}{
		{"missing both", ""},
		{"missing code", "?state=abc"},
		{"missing state", "?code=abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback"+tt.query, nil)
			rec := httptest.NewRecorder()
			h.Callback(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestGoogleOAuth_ReplayPrevention(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)

	state := "replay-test-state"
	_, err := db.Exec(
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, '', '', '/')`,
		state)
	require.NoError(t, err)

	// First callback consumes the state
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?state="+state+"&code=test-code", nil)
	rec := httptest.NewRecorder()
	h.Callback(rec, req)

	// Second callback with same state should fail (replay)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?state="+state+"&code=test-code", nil)
	rec = httptest.NewRecorder()
	h.Callback(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
