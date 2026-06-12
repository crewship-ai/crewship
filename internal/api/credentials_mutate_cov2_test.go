package api

// Third-wave coverage for credentials_mutate.go: Create's nil-user 401,
// the soft-delete-cleanup warn, the insert/oauth-fields/crew-junction
// failure 500s, the audit-event warn; Update's CERTIFICATE validation,
// crew_ids validation errors, the non-numeric security_level 400, and the
// update/crew-junction failure 500s + inline-rotation audit warn.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covCM3Rig(t *testing.T) (h *CredentialHandler, userID, wsID string) {
	t.Helper()
	h, db := newCredHandler(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	return h, userID, wsID
}

func TestCM3_Create_NoUser401(t *testing.T) {
	h, _, wsID := covCM3Rig(t)
	req := httptest.NewRequest("POST", "/api/v1/credentials",
		strings.NewReader(`{"name":"x","value":"v","type":"API_KEY"}`))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Create_SoftDeleteCleanupWarn_StillCreates(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	// Block only deletes of soft-deleted rows; the create itself proceeds.
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_cleanup BEFORE DELETE ON credentials
		WHEN OLD.deleted_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'cm3 no cleanup'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMCreate(t, h, userID, wsID, "OWNER", `{"name":"cm3-a","value":"v","type":"API_KEY","provider":"CUSTOM"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Create_InsertFails500(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_insert BEFORE INSERT ON credentials
		BEGIN SELECT RAISE(ABORT, 'cm3 no inserts'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMCreate(t, h, userID, wsID, "OWNER", `{"name":"cm3-b","value":"v","type":"API_KEY"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Create_OAuthFieldsUpdateFails500(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_oauth BEFORE UPDATE OF oauth_client_id ON credentials
		BEGIN SELECT RAISE(ABORT, 'cm3 no oauth'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	body := `{"name":"cm3-oauth","value":"v","type":"OAUTH2","oauth_client_id":"cid","oauth_client_secret":"sec","oauth_auth_url":"https://a","oauth_token_url":"https://t"}`
	rr := covCMCreate(t, h, userID, wsID, "OWNER", body)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Create_CrewJunctionFails500(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	covCMSeedCrew(t, h.db, wsID, "crew-cm3")
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_junction BEFORE INSERT ON credential_crews
		BEGIN SELECT RAISE(ABORT, 'cm3 no junction'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMCreate(t, h, userID, wsID, "OWNER",
		`{"name":"cm3-c","value":"v","type":"API_KEY","crew_ids":["crew-cm3"]}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Create_AuditEventWarn_StillCreates(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_audit BEFORE INSERT ON credential_audit
		BEGIN SELECT RAISE(ABORT, 'cm3 no audit'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMCreate(t, h, userID, wsID, "OWNER", `{"name":"cm3-d","value":"v","type":"API_KEY"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 despite audit failure; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Update ----

// covCM3Seed creates a baseline API_KEY credential through the handler so
// every column the Update path reads is present.
func covCM3Seed(t *testing.T, h *CredentialHandler, userID, wsID, name string) string {
	t.Helper()
	rr := covCMCreate(t, h, userID, wsID, "OWNER", `{"name":"`+name+`","value":"v","type":"API_KEY","provider":"CUSTOM"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed credential: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var id string
	if err := h.db.QueryRow(`SELECT id FROM credentials WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatalf("read credential id: %v", err)
	}
	return id
}

func TestCM3_Update_CertificateValidation(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-cert")

	t.Run("type change without value", func(t *testing.T) {
		rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"type":"CERTIFICATE"}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "requires a new value") {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("non-PEM value", func(t *testing.T) {
		rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"type":"CERTIFICATE","value":"not-a-pem"}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "PEM-encoded") {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCM3_Update_CrewIDsValidation(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-crews")

	t.Run("unknown crew 400", func(t *testing.T) {
		rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"crew_ids":["ghost-crew"]}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Invalid crew_id") {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("crew lookup DB error 500", func(t *testing.T) {
		if _, err := h.db.Exec(`ALTER TABLE crews RENAME TO crews_hidden_cm3`); err != nil {
			t.Fatalf("rename crews: %v", err)
		}
		t.Cleanup(func() { _, _ = h.db.Exec(`ALTER TABLE crews_hidden_cm3 RENAME TO crews`) })
		rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"crew_ids":["any-crew"]}`)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCM3_Update_SecurityLevelNotANumber400(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-sec")
	rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"security_level":"high"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "security_level must be 1, 2, or 3") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Update_ExecFails500(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-upd")
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_update BEFORE UPDATE OF description ON credentials
		BEGIN SELECT RAISE(ABORT, 'cm3 no update'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"description":"new"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Update_CrewJunctionFails500(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-jct")
	covCMSeedCrew(t, h.db, wsID, "crew-cm3-j")
	// BEFORE DELETE wouldn't fire here (no existing junction rows), so
	// block the INSERT that setCrewIDs issues next.
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_junction2 BEFORE INSERT ON credential_crews
		BEGIN SELECT RAISE(ABORT, 'cm3 no junction'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"crew_ids":["crew-cm3-j"]}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCM3_Update_InlineRotateAuditWarn_StillUpdates(t *testing.T) {
	h, userID, wsID := covCM3Rig(t)
	credID := covCM3Seed(t, h, userID, wsID, "cm3-rot")
	// Fail only ROTATE audit inserts; the value rewrite itself commits.
	if _, err := h.db.Exec(`
		CREATE TRIGGER cm3_block_rotate BEFORE INSERT ON credential_audit
		WHEN NEW.event_type = 'ROTATE'
		BEGIN SELECT RAISE(ABORT, 'cm3 no rotate audit'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covCMUpdate(t, h, userID, wsID, "OWNER", credID, `{"value":"rotated-value"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite audit failure; body=%s", rr.Code, rr.Body.String())
	}
}
