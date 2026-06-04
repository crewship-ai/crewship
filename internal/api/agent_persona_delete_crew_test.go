package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// DeleteCrewPersona (DELETE /api/v1/crews/{crewId}/persona) resets the
// crew persona layer to default; 404 for an unknown crew, 204 on success.

func TestDeleteCrewPersona_HappyPath(t *testing.T) {
	r := newPersonaTestRig(t)

	// Write a crew persona first so the layer exists; DeleteCrewPersona
	// then resets it back to default.
	putRec := httptest.NewRecorder()
	r.h.PutCrewPersona(putRec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": "Crew-wide: ship small, test first.",
	}))
	if putRec.Code != http.StatusOK {
		t.Fatalf("seed PUT crew persona: status=%d body=%s", putRec.Code, putRec.Body.String())
	}

	rec := httptest.NewRecorder()
	r.h.DeleteCrewPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteCrewPersona_NotFound(t *testing.T) {
	r := newPersonaTestRig(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/crews/ghost/persona", nil)
	req.SetPathValue("crewId", "ghost")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	r.h.DeleteCrewPersona(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rec.Code, rec.Body.String())
	}
}
