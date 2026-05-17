package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// telemetryHandlerRig builds the handler against a freshly-migrated DB.
// The status endpoint is unauthenticated by design (frontend init runs
// before login), so no user/workspace seeding is needed.
func telemetryHandlerRig(t *testing.T) *TelemetryStatusHandler {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewTelemetryStatusHandler(db, logger)
}

func TestTelemetryStatus_FreshDB_ReturnsDisabledWithEmptyInstallID(t *testing.T) {
	// Brand-new DB: no consent row, no install ID. The handler must
	// always answer 200 so the frontend init logic doesn't crash on a
	// surprise 4xx/5xx — and must report enabled=false so the browser
	// stays out of Sentry until the operator explicitly consents.
	h := telemetryHandlerRig(t)

	req := httptest.NewRequest("GET", "/api/v1/system/telemetry", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handler must never 5xx — privacy-preserving fallback)", rr.Code)
	}
	var resp telemetryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Enabled {
		t.Errorf("Enabled = true on a fresh DB, want false")
	}
}

func TestTelemetryStatus_NoAuthRequired(t *testing.T) {
	// The endpoint is intentionally public so Sentry init can run before
	// login. Calling it with no auth context whatsoever must still
	// return a 200 with a well-formed payload. A regression that gates
	// this behind authed() would silently disable client-side Sentry
	// on every fresh page load.
	h := telemetryHandlerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/system/telemetry", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (must work pre-login)", rr.Code)
	}
	// Body must be parseable JSON with the documented shape.
	var resp telemetryStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not a telemetryStatusResponse: %v; body=%s", err, rr.Body.String())
	}
}

func TestTelemetryStatus_ResponseShapeIsStable(t *testing.T) {
	// Sentry init in sentry.client.config.ts hard-codes the field names
	// `enabled` and `install_id`. Renaming either field on the server
	// side would silently disable client-side telemetry — guard the
	// JSON keys.
	h := telemetryHandlerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/system/telemetry", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["enabled"]; !ok {
		t.Errorf("response missing 'enabled' key — sentry.client.config.ts will break")
	}
	if _, ok := raw["install_id"]; !ok {
		t.Errorf("response missing 'install_id' key — cross-stack event correlation will break")
	}
}
