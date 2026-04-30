package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "0", rec.Header().Get("X-XSS-Protection"))
	assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, "camera=(), microphone=(), geolocation=()", rec.Header().Get("Permissions-Policy"))

	// Verify HSTS is NOT set (binary may run on HTTP)
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	// Audit M5: API router fronts only JSON/SSE/WS responses, so the strict
	// default-src 'none' policy is the right baseline. The SPA gets a
	// looser CSP from server.securityHeadersMiddleware.
	assert.Equal(t,
		"default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
		rec.Header().Get("Content-Security-Policy"))
}

func TestSecurityHeaders_PreservesBody(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"status":"ok"}`, rec.Body.String())
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

// TestSecurityHeaders_ExposedPathSkipsCSP regresses the CodeRabbit round 2
// finding on PR #236: /exposed/ is mounted on the API router but reverse-
// proxies arbitrary upstream apps. The strict default-src 'none' would
// break any HTML UI served through the proxy, so SecurityHeaders MUST
// skip the CSP header on those paths while still applying every other
// hardening header.
func TestSecurityHeaders_ExposedPathSkipsCSP(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/exposed/abc123/index.html", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Content-Security-Policy"),
		"CSP must not be stamped on /exposed/ responses — upstream owns its policy")
	// Other hardening headers still apply.
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
}
