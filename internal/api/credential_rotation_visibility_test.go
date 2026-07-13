package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListRotations_VisibilityGate covers #1066: ListRotations must apply the
// same role/visibility filter as the credential Get handler. A CREW-scoped
// credential's rotation history must NOT be readable by a workspace member who
// isn't in that crew (VIEWER/MEMBER), while MANAGER+ retain full visibility.
func TestListRotations_VisibilityGate(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	// A CREW-scoped credential attached to a crew the VIEWER is not in.
	credID := "cred-rot-vis"
	seedCredentialEnc(t, db, wsID, ownerID, credID, "TEST_KEY", "v")
	if _, err := db.Exec(`UPDATE credentials SET scope = 'CREW' WHERE id = ?`, credID); err != nil {
		t.Fatalf("scope: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-vis', ?, 'Vis', 'crew-vis')`, wsID); err != nil {
		t.Fatalf("crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, 'crew-vis')`, credID); err != nil {
		t.Fatalf("credential_crews: %v", err)
	}

	// A VIEWER in the workspace but not a member of crew-vis.
	viewerID := "viewer-vis"
	if _, err := db.Exec(`INSERT INTO users (id, email, created_at, updated_at) VALUES (?, 'viewer-vis@example.com', datetime('now'), datetime('now'))`, viewerID); err != nil {
		t.Fatalf("viewer user: %v", err)
	}

	h := NewCredentialHandler(db, covCRLogger())

	newReq := func(role, userID string) *http.Request {
		req := httptest.NewRequest("GET", "/api/v1/credentials/"+credID+"/rotations", nil)
		ctx := withUser(req.Context(), &AuthUser{ID: userID})
		ctx = withWorkspace(ctx, wsID, role)
		req = req.WithContext(ctx)
		req.SetPathValue("credentialId", credID)
		return req
	}

	// VIEWER not in the crew must NOT see the credential's rotation history.
	t.Run("viewer without crew membership → 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListRotations(rec, newReq("VIEWER", viewerID))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("VIEWER status = %d body=%s, want 404", rec.Code, rec.Body.String())
		}
	})

	// Allow path: a VIEWER who IS a member of the credential's crew must see
	// the history. Without this, a broken credential_crews/crew_members join
	// (always-false) would still pass the deny + OWNER cases above.
	t.Run("viewer with crew membership → 200", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO crew_members (crew_id, user_id) VALUES ('crew-vis', ?)`, viewerID); err != nil {
			t.Fatalf("crew_members: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ListRotations(rec, newReq("VIEWER", viewerID))
		if rec.Code != http.StatusOK {
			t.Fatalf("VIEWER (member) status = %d body=%s, want 200", rec.Code, rec.Body.String())
		}
	})

	// OWNER (manage tier) retains full visibility.
	t.Run("owner → 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListRotations(rec, newReq("OWNER", ownerID))
		if rec.Code != http.StatusOK {
			t.Fatalf("OWNER status = %d body=%s, want 200", rec.Code, rec.Body.String())
		}
		var out []rotationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	})
}
