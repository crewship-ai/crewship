package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func postRelease(t *testing.T, h *AgentHandler, userID, wsID, role, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/release", nil)
	req.SetPathValue("agentId", agentID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	req = req.WithContext(withWorkspace(ctx, wsID, role))
	rr := httptest.NewRecorder()
	h.Release(rr, req)
	return rr
}

// "Release now" ends a temporary hire before its time: the agent becomes an
// expired hire at once (runs and notes stay, Rehire is offered), instead of
// waiting for the five-minute expiry sweep.
func TestRelease(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	past := "2024-01-01T00:00:00Z"
	tests := []struct {
		name        string
		role        string
		permanent   bool
		status      string
		expiresAt   *string
		expiredAt   *string
		agentID     string // empty = the seeded one
		wantCode    int
		wantExpired bool
	}{
		{name: "live hire is released", role: "MANAGER", status: "IDLE", expiresAt: &future, wantCode: 200, wantExpired: true},
		{name: "already expired is a no-op", role: "MANAGER", status: "IDLE", expiresAt: &past, expiredAt: &past, wantCode: 200, wantExpired: true},
		{name: "viewers cannot release", role: "VIEWER", status: "IDLE", expiresAt: &future, wantCode: 403},
		{name: "a running hire must stop first", role: "MANAGER", status: "RUNNING", expiresAt: &future, wantCode: 409},
		{name: "a hire waiting for approval is denied, not released", role: "MANAGER", status: "PENDING_REVIEW", expiresAt: &future, wantCode: 409},
		{name: "a permanent agent is not a hire", role: "MANAGER", permanent: true, status: "IDLE", wantCode: 404},
		{name: "unknown agent", role: "MANAGER", agentID: "agent-nope", wantCode: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
			h := newHireHandler(t, db)
			id := seedEphemeralAgent(t, db, wsID, crewID, "rel-eph-aaaaaa", tc.expiresAt, tc.expiredAt, "[x] hire: test")
			execOrFatal(t, db, `UPDATE agents SET status = ?, ephemeral = ? WHERE id = ?`, tc.status, map[bool]int{true: 0, false: 1}[tc.permanent], id)
			if tc.agentID != "" {
				id = tc.agentID
			}
			rr := postRelease(t, h, userID, wsID, tc.role, id)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var expiredAt sql.NullString
			if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, id).Scan(&expiredAt); err != nil {
				t.Fatal(err)
			}
			if expiredAt.Valid != tc.wantExpired {
				t.Errorf("expired_at valid = %v, want %v", expiredAt.Valid, tc.wantExpired)
			}
			var reason string
			_ = db.QueryRow(`SELECT hire_reason FROM agents WHERE id = ?`, id).Scan(&reason)
			if tc.expiredAt == nil && reason == "[x] hire: test" {
				t.Errorf("release left no line in the hire history")
			}
		})
	}
}
