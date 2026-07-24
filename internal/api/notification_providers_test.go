package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifyProvidersHandler_List_DefaultsAllEnabled(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyProvidersHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-providers", nil), "u1", "ws1", "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Providers []providerInfo `json:"providers"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Providers) != 3 {
		t.Fatalf("expected 3 supported providers, got %d", len(resp.Providers))
	}
	for _, p := range resp.Providers {
		if !p.Enabled {
			t.Errorf("provider %q should default to enabled, got disabled", p.Provider)
		}
	}
}

func TestNotifyProvidersHandler_Patch_DisablesThenReflectsInList(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyProvidersHandler(db, newTestLogger())

	patchReq := withWorkspaceUser(httptest.NewRequest("PATCH", "/api/v1/notification-providers/slack", strings.NewReader(`{"enabled":false}`)), "u1", "ws1", "ADMIN")
	patchReq.SetPathValue("provider", "slack")
	patchRR := httptest.NewRecorder()
	h.Patch(patchRR, patchReq)
	if patchRR.Code != 200 {
		t.Fatalf("patch: got %d, body=%s", patchRR.Code, patchRR.Body.String())
	}

	listReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-providers", nil), "u1", "ws1", "MEMBER")
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	var resp struct {
		Providers []providerInfo `json:"providers"`
	}
	_ = json.Unmarshal(listRR.Body.Bytes(), &resp)
	for _, p := range resp.Providers {
		if p.Provider == "slack" && p.Enabled {
			t.Fatal("slack should now be disabled")
		}
	}
}

func TestNotifyProvidersHandler_Patch_UnknownProvider404s(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyProvidersHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/api/v1/notification-providers/carrier-pigeon", strings.NewReader(`{"enabled":false}`)), "u1", "ws1", "ADMIN")
	req.SetPathValue("provider", "carrier-pigeon")
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	if rr.Code != 404 {
		t.Fatalf("expected 404 for unknown provider, got %d", rr.Code)
	}
}
