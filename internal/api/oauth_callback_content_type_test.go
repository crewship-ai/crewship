package api

// #2102: GET /api/v1/oauth/callback answered every error branch with raw
// http.Error, which writes Content-Type: text/plain; charset=utf-8. The
// generated OpenAPI document declares application/json for every non-2xx
// response on every route (cmd/gen-openapi/main.go, #1919) on the stated
// grounds that both error helpers (replyError / writeProblem) route
// through writeJSON — Callback reached for neither, so it was the one
// path the invariant didn't know about, and the API contract gate's
// "Undocumented Content-Type" finding for this path was real, not a
// harness artifact.
//
// The fix routes every error branch through replyError, the same helper
// the sibling Initiate/Exchange handlers in this file already use, so the
// success branch's deliberate text/html (a browser followed a redirect
// here) stays the only non-JSON response Callback ever sends.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthCallback_ErrorResponses_AreJSON(t *testing.T) {
	h, db, userID, wsID := covOAuthRig(t)
	covSeedOAuthCred(t, db, wsID, userID, "cred-ct", "https://p.example/auth", "://bad")

	run := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/oauth/callback"+query, nil)
		rr := httptest.NewRecorder()
		h.Callback(rr, req)
		return rr
	}

	cases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"provider error param", "?error=access_denied", http.StatusBadRequest},
		{"missing code/state", "?code=abc", http.StatusBadRequest},
		{"bad/unknown state token", "?code=abc&state=ghost", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := run(c.query)
			if rr.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, c.wantStatus, rr.Body.String())
			}
			gotCT := rr.Header().Get("Content-Type")
			if !strings.HasPrefix(gotCT, "application/json") {
				t.Errorf("Content-Type = %q, want application/json (spec declares application/json for every non-2xx response, #1919)", gotCT)
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Errorf("body is not valid JSON: %v; body=%q", err, rr.Body.String())
			}
			if _, ok := body["error"]; !ok {
				t.Errorf("body = %v, want the canonical {\"error\": ...} shape replyError writes", body)
			}
		})
	}
}
