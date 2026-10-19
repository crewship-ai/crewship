package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// A MANAGER can preview automations but cannot read administrative capability sets;
// previewing the same durable event must not bypass that boundary.
func TestAuthorizationAuditPreviewDoesNotExposeCapabilitySets(t *testing.T) {
	f := newAuthorizationAuditFixture(t)
	if rr := f.do("PATCH", "/members/"+f.member, `{"role":"MANAGER"}`); rr.Code != 200 {
		t.Fatalf("manager role calibration status %d", rr.Code)
	}
	if rr := f.do("PATCH", "/members/"+f.target+"/capabilities", `{"grant":["issue.create"]}`); rr.Code != 200 {
		t.Fatalf("owner mutation status %d", rr.Code)
	}
	var raw string
	if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.capabilities_changed'`, f.workspace).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal([]byte(raw), &canonical); err != nil {
		t.Fatal(err)
	}
	if len(canonical["previous_capabilities"].([]any)) != 2 || len(canonical["capabilities"].([]any)) != 3 {
		t.Fatal("missing canonical before/after sets")
	}
	f.checkChain(t)
	validator, err := auth.NewJWTValidator("administration-audit-test-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(f.db).Create(t.Context(), f.target, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(f.target, session.ID, "Member", "member@test.invalid")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(f.db, "administration-audit-test-signing-key", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Workspace-ID", f.workspace)
		r.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, r)
		return rr
	}
	if rr := request("GET", "/api/v1/workspaces/"+f.workspace+"/members/"+f.target+"/capabilities", ""); rr.Code != 403 {
		t.Fatalf("manager capability calibration status %d", rr.Code)
	}
	rr := request("POST", "/api/v1/automations/preview", `{"event_type":"member.capabilities_changed","matcher":{}}`)
	if rr.Code != 200 {
		t.Fatalf("manager preview status %d", rr.Code)
	}
	var response struct {
		Scanned int `json:"scanned"`
		Matched int `json:"matched"`
		Samples []struct {
			Payload map[string]any `json:"payload"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Scanned != 1 || response.Matched != 1 || len(response.Samples) != 1 {
		t.Fatalf("preview calibration scanned=%d matched=%d samples=%d", response.Scanned, response.Matched, len(response.Samples))
	}
	for _, key := range []string{"previous_capabilities", "capabilities"} {
		if _, exposed := response.Samples[0].Payload[key]; exposed {
			t.Errorf("manager preview exposed private audit field %s", key)
		}
	}
	var after string
	if err := f.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.capabilities_changed'`, f.workspace).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != raw {
		t.Fatal("preview changed canonical audit")
	}
}
