package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnforceOrigin_GETPasses(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://crewship.local/api/v1/anything", nil)
	req.Host = "crewship.local"
	req.Header.Set("Origin", "https://attacker.example") // GET is exempt; cross-origin OK
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceOrigin_PostSameOriginPasses(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://crewship.local/api/v1/x", nil)
	req.Host = "crewship.local"
	req.Header.Set("Origin", "http://crewship.local")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// Regression for F-006: a cross-site POST with a malicious Origin must be
// 403'd by the backend even if the cookie would have been included.
func TestEnforceOrigin_PostCrossOriginRejected(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodDelete, "http://crewship.local/api/v1/agents/x", nil)
	req.Host = "crewship.local"
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"cross-origin DELETE must be rejected by backend even though SameSite=Lax already withholds the cookie")
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "origin_rejected")
}

// TestEnforceOrigin_NullOriginRejected — sandboxed iframe attack vector.
// data: URIs, srcdoc, and file:// pages send `Origin: null`. Pre-fix
// requestOrigin treated "null" as "no Origin header" and the request
// passed through. CodeRabbit caught it on the first review pass —
// "null" must compare against the allowlist (where it can never
// match) and the request must be 403'd.
func TestEnforceOrigin_NullOriginRejected(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://crewship.local/api/v1/x", nil)
	req.Host = "crewship.local"
	req.Header.Set("Origin", "null")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"Origin: null (sandboxed iframe) must be treated as cross-origin, not absent")
}

func TestEnforceOrigin_PostNoOriginPasses(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://crewship.local/api/v1/x", nil)
	req.Host = "crewship.local"
	// No Origin / Referer — typical for CLI tokens, sidecar IPC, and curl.
	// Browsers always send Origin on cross-origin POSTs, so the absence
	// is a strong "this isn't a browser CSRF" signal.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceOrigin_RefererFallback(t *testing.T) {
	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Cross-origin Referer = reject (no Origin header, fall back to Referer)
	req := httptest.NewRequest(http.MethodPost, "http://crewship.local/api/v1/x", nil)
	req.Host = "crewship.local"
	req.Header.Set("Referer", "https://evil.example/something/page")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Same-origin Referer = pass
	req = httptest.NewRequest(http.MethodPost, "http://crewship.local/api/v1/x", nil)
	req.Host = "crewship.local"
	req.Header.Set("Referer", "http://crewship.local/page")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceOrigin_AllowedOriginsList(t *testing.T) {
	// Override under the shared test-globals mutex (see
	// withAllowedOriginSuffixes) so this stays parallel-safe; restored
	// via t.Cleanup. CodeRabbit's R2 review caught the unguarded
	// mutation pattern.
	withAllowedOriginSuffixes(t, []string{"https://app.crewship.io", "https://staging.crewship.io"})

	h := EnforceOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, ok := range []string{"https://app.crewship.io", "https://staging.crewship.io"} {
		req := httptest.NewRequest(http.MethodPatch, "http://api.crewship.io/api/v1/x", nil)
		req.Host = "api.crewship.io"
		req.Header.Set("Origin", ok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "allowlisted origin %s should pass", ok)
	}
	// Not in the list and not same-origin → reject
	req := httptest.NewRequest(http.MethodPatch, "http://api.crewship.io/api/v1/x", nil)
	req.Host = "api.crewship.io"
	req.Header.Set("Origin", "https://random.tld")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestParseAllowedOrigins(t *testing.T) {
	got := parseAllowedOrigins(" https://a.com/, ,https://b.com")
	assert.Equal(t, []string{"https://a.com", "https://b.com"}, got)
	assert.Nil(t, parseAllowedOrigins(""))
	assert.Nil(t, parseAllowedOrigins("   "))
}

func TestExpectedSelfOrigin_PrefersTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "api.crewship.io"
	assert.Equal(t, "http://api.crewship.io", expectedSelfOrigin(req))
}
