package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Version is unauthenticated-safe only for authed callers: it 401s when
// no user is on the context, otherwise reports the running version with
// latest=null (the update.Check call is best-effort and degrades to nil
// rather than blocking the response).

func TestSystemVersion_RequiresUser(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "v1.2.3")
	req := httptest.NewRequest("GET", "/api/v1/system/version", nil)
	rr := httptest.NewRecorder()
	h.Version(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSystemVersion_ReportsCurrent(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "v1.2.3")
	req := httptest.NewRequest("GET", "/api/v1/system/version", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Version(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["current"] != "v1.2.3" {
		t.Errorf("current=%v want v1.2.3", resp["current"])
	}
	if _, ok := resp["latest"]; !ok {
		t.Errorf("response missing 'latest' key: %v", resp)
	}
}
