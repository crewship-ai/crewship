package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

// TestAdminObservability_Health_ReportsEncryptionKeySource: the health
// payload must carry where the master key came from ("external" |
// "generated" | "unknown") so operators can spot a key colocated with the
// database without shelling into the host (E2).
func TestAdminObservability_Health_ReportsEncryptionKeySource(t *testing.T) {
	h := NewAdminObservabilityHandler(nil, logging.New("info", "json", nil))
	h.dataDir = func() (string, error) { return "/fake/data", nil }

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/health", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, "OWNER"))
	w := httptest.NewRecorder()
	h.Health(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		EncryptionKeySource string `json:"encryption_key_source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The secrets bootstrap doesn't run in unit tests, so the truthful
	// answer here is "unknown" — the field must be present either way.
	valid := map[string]bool{"external": true, "generated": true, "unknown": true}
	if !valid[resp.EncryptionKeySource] {
		t.Fatalf("encryption_key_source = %q, want one of external|generated|unknown", resp.EncryptionKeySource)
	}
}
