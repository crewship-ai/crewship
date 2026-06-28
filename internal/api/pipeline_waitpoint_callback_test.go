package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CompleteWaitpointToken is the public, token-authed completion endpoint.
// These cover the guard branches without a DB; the happy-path completion
// is exercised end-to-end (the store path is shared with ApproveWaitpoint).

func TestCompleteWaitpointToken_NoStore_Returns503(t *testing.T) {
	h := &PipelineHandler{} // waitpoints nil
	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitpoint-tokens/tok_x", strings.NewReader(`{}`))
	req.SetPathValue("token", "tok_x")
	rr := httptest.NewRecorder()
	h.CompleteWaitpointToken(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: got %d, want 503", rr.Code)
	}
}

func TestCompleteWaitpointToken_MissingToken_Returns400(t *testing.T) {
	// A non-nil waitpoint store so we reach the token check. The fake
	// only needs to be non-nil; the handler returns 400 before using it.
	h := &PipelineHandler{waitpoints: stubBareWaitpoints{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitpoint-tokens/", strings.NewReader(`{}`))
	req.SetPathValue("token", "")
	rr := httptest.NewRecorder()
	h.CompleteWaitpointToken(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing token: got %d, want 400", rr.Code)
	}
}
