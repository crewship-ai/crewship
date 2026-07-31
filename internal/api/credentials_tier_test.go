package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The credential tier, at the write boundary.
//
// The regression this file exists for: `security_level: 4` fell outside the
// create path's 1..3 range check and was silently replaced with 1. Marking a
// production-admin credential CRITICAL therefore gave it the LOWEST tier, and the
// API answered 201. Now that L4 has consequences (every read becomes a human
// approval, internal/keeper/tier.go) that is a security bug, not a cosmetic one.

func tierCredHandler(t *testing.T) (*CredentialHandler, string, string) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("0123456789abcdef", 4))
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewCredentialHandler(db, newTestLogger()), userID, wsID
}

func createCredWithLevel(t *testing.T, h *CredentialHandler, userID, wsID, name, level string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"name":"` + name + `","value":"s3cret","type":"API_KEY","scope":"WORKSPACE"`
	if level != "" {
		body += `,"security_level":` + level
	}
	body += `}`
	rr := httptest.NewRecorder()
	h.Create(rr, withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/credentials", strings.NewReader(body)),
		userID, wsID, "OWNER"))
	return rr
}

func storedLevel(t *testing.T, h *CredentialHandler, wsID, name string) int {
	t.Helper()
	var lvl int
	if err := h.db.QueryRow(
		`SELECT security_level FROM credentials WHERE workspace_id = ? AND name = ?`, wsID, name).Scan(&lvl); err != nil {
		t.Fatalf("read stored level: %v", err)
	}
	return lvl
}

// The tier an operator asks for is the tier they get.
func TestCredentialTier_CriticalIsStoredNotDowngraded(t *testing.T) {
	h, userID, wsID := tierCredHandler(t)

	rr := createCredWithLevel(t, h, userID, wsID, "PROD_DB_ADMIN", "4")
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := storedLevel(t, h, wsID, "PROD_DB_ADMIN"); got != 4 {
		t.Errorf("stored level = %d, want 4 — a credential marked critical was filed as %d", got, got)
	}
}

// Every defined tier round-trips. A create path that accepts three of four tiers
// is one an operator cannot trust for the fourth.
func TestCredentialTier_EveryTierRoundTrips(t *testing.T) {
	for _, lvl := range []string{"1", "2", "3", "4"} {
		t.Run("L"+lvl, func(t *testing.T) {
			h, userID, wsID := tierCredHandler(t)
			rr := createCredWithLevel(t, h, userID, wsID, "CRED_"+lvl, lvl)
			if rr.Code != http.StatusCreated {
				t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
			}
			want := int(lvl[0] - '0')
			if got := storedLevel(t, h, wsID, "CRED_"+lvl); got != want {
				t.Errorf("stored level = %d, want %d", got, want)
			}
		})
	}
}

// A level outside the table is refused rather than quietly replaced, and the
// message names the tiers — "must be 1..4" tells an operator the shape of the
// field and nothing about which one their credential is.
func TestCredentialTier_OutOfRangeIsRefusedWithTheTiersNamed(t *testing.T) {
	for _, lvl := range []string{"0", "5", "-1", "99"} {
		t.Run(lvl, func(t *testing.T) {
			h, userID, wsID := tierCredHandler(t)
			rr := createCredWithLevel(t, h, userID, wsID, "CRED_BAD", lvl)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("create status = %d, want 400: %s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			for _, want := range []string{"critical", "low"} {
				if !strings.Contains(body, want) {
					t.Errorf("rejection %q does not name the tiers (missing %q)", body, want)
				}
			}
		})
	}
}

// Omitting the field keeps the shipped default, so nothing an existing client
// sends changes meaning.
func TestCredentialTier_OmittedDefaultsToL1(t *testing.T) {
	h, userID, wsID := tierCredHandler(t)
	rr := createCredWithLevel(t, h, userID, wsID, "PLAIN_KEY", "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := storedLevel(t, h, wsID, "PLAIN_KEY"); got != 1 {
		t.Errorf("stored level = %d, want the default 1", got)
	}
}

// Raising an existing credential to critical is the common operation — an
// operator realises what a credential actually reaches — so PATCH has to accept
// the tier the create path now does.
func TestCredentialTier_PatchCanRaiseToCritical(t *testing.T) {
	h, userID, wsID := tierCredHandler(t)
	if rr := createCredWithLevel(t, h, userID, wsID, "SSH_KEY", "2"); rr.Code != http.StatusCreated {
		t.Fatalf("seed create status = %d: %s", rr.Code, rr.Body.String())
	}
	var credID string
	if err := h.db.QueryRow(`SELECT id FROM credentials WHERE workspace_id = ? AND name = 'SSH_KEY'`, wsID).Scan(&credID); err != nil {
		t.Fatalf("find credential: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/api/v1/credentials/"+credID,
		strings.NewReader(`{"security_level":4}`))
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Update(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := storedLevel(t, h, wsID, "SSH_KEY"); got != 4 {
		t.Errorf("stored level = %d, want 4", got)
	}

	// And a bad one is still refused, so the PATCH path cannot be the way around
	// the create path's validation.
	req = httptest.NewRequest("PATCH", "/api/v1/credentials/"+credID,
		strings.NewReader(`{"security_level":9}`))
	req.SetPathValue("credentialId", credID)
	rr = httptest.NewRecorder()
	h.Update(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("patch with level 9 status = %d, want 400", rr.Code)
	}
	if got := storedLevel(t, h, wsID, "SSH_KEY"); got != 4 {
		t.Errorf("a refused patch changed the stored level to %d", got)
	}
}

// The console cannot render or edit a tier it cannot read. It had no tier control
// at all until this slice, which is why every credential created through the UI
// was L1 — including the production ones.
func TestCredentialTier_ReadPathsCarryTheTierAndItsLabel(t *testing.T) {
	h, userID, wsID := tierCredHandler(t)
	if rr := createCredWithLevel(t, h, userID, wsID, "PROD_ADMIN", "4"); rr.Code != http.StatusCreated {
		t.Fatalf("seed create status = %d: %s", rr.Code, rr.Body.String())
	}

	rr := httptest.NewRecorder()
	h.List(rr, withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/credentials", nil), userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"security_level":4`) {
		t.Errorf("list response does not carry the tier: %s", body)
	}
	// The label is served rather than mapped client-side so the console, the CLI
	// and the judge prompt cannot drift into three vocabularies.
	if !strings.Contains(body, "critical") {
		t.Errorf("list response does not carry the tier label: %s", body)
	}
}
