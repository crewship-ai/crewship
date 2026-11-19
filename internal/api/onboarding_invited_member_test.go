package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Issue #4876: someone who joined a workspace somebody else set up — and owns
// none of their own — has nothing to set up. Status must report onboarding
// as done for them (so the dashboard does not send them into the "Set up
// Crewship" wizard) and must not create the Crewship Guide crew as a side
// effect of their read. The owner of the same workspace keeps the wizard.
func TestOnboardingStatus_InvitedMemberOfExistingWorkspace(t *testing.T) {
	for _, tc := range []struct {
		role          string
		wantCompleted bool
		wantSetupCrew int
	}{
		{role: "OWNER", wantCompleted: false, wantSetupCrew: 1},
		{role: "ADMIN", wantCompleted: true, wantSetupCrew: 0},
		{role: "MANAGER", wantCompleted: true, wantSetupCrew: 0},
		{role: "MEMBER", wantCompleted: true, wantSetupCrew: 0},
		{role: "VIEWER", wantCompleted: true, wantSetupCrew: 0},
	} {
		t.Run(tc.role, func(t *testing.T) {
			setTestEncryptionKeyParallelSafe(t)
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			if err := insertOnboardingCredential(context.Background(), db, ownerID, wsID, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
				t.Fatalf("insert credential: %v", err)
			}

			callerID := ownerID
			if tc.role != "OWNER" {
				callerID = "invited-user-id"
				if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'invited@example.com', 'Invited User')`, callerID); err != nil {
					t.Fatalf("insert invited user: %v", err)
				}
				if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-invited', ?, ?, ?)`, wsID, callerID, tc.role); err != nil {
					t.Fatalf("insert invited member: %v", err)
				}
			}

			h := NewOnboardingHandler(db, nil, testLogger())
			req := httptest.NewRequest(http.MethodGet, "/api/v1/onboarding/status", nil)
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: callerID}))
			w := httptest.NewRecorder()
			h.Status(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			var body map[string]bool
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["completed"] != tc.wantCompleted {
				t.Errorf("completed = %v, want %v", body["completed"], tc.wantCompleted)
			}
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
				t.Fatalf("count setup crews: %v", err)
			}
			if n != tc.wantSetupCrew {
				t.Errorf("setup crews = %d, want %d", n, tc.wantSetupCrew)
			}
		})
	}
}
