package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProvisionStatus_ResolvedFeaturesDistinguishesNoneFromUnknown pins the
// distinction the resolved_features column exists to carry.
//
// NULL means "provisioned before provenance was recorded" — we do not know
// what is in the image. '[]' means "this build installed no features" — we do
// know, and the answer is none. Collapsing the two makes `crew provision
// status` report a featureless crew as unaudited forever, since a crew with
// no features would never write the column at all.
func TestProvisionStatus_ResolvedFeaturesDistinguishesNoneFromUnknown(t *testing.T) {
	tests := []struct {
		name       string
		column     any // nil → SQL NULL
		wantKey    bool
		wantLength int
	}{
		{
			name:    "never recorded stays unknown",
			column:  nil,
			wantKey: false,
		},
		{
			name:       "built with no features answers none",
			column:     "[]",
			wantKey:    true,
			wantLength: 0,
		},
		{
			name:       "built with features answers with them",
			column:     `[{"ref":"ghcr.io/x/common-utils:2","id":"common-utils","version":"2.5.4","digest":"sha256:abc","pinned":false}]`,
			wantKey:    true,
			wantLength: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestProvisioningHandler(t)
			userID := seedTestUser(t, h.db)
			wsID := seedTestWorkspace(t, h.db, userID)
			crewID := seedCrewRow(t, h.db, "crew-feat", wsID, "Feat", "feat")

			if _, err := h.db.Exec(`UPDATE crews SET resolved_features = ? WHERE id = ?`, tt.column, crewID); err != nil {
				t.Fatalf("seeding resolved_features: %v", err)
			}

			req := httptest.NewRequest("GET", "/x", nil)
			req.SetPathValue("crewId", crewID)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ProvisionStatus(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding response: %v", err)
			}

			raw, present := body["resolved_features"]
			if present != tt.wantKey {
				t.Fatalf("resolved_features present = %v, want %v (body=%s)", present, tt.wantKey, rr.Body.String())
			}
			if !tt.wantKey {
				return
			}
			list, ok := raw.([]any)
			if !ok {
				t.Fatalf("resolved_features = %T (%v), want a JSON array — null would read as unknown", raw, raw)
			}
			if len(list) != tt.wantLength {
				t.Errorf("resolved_features has %d entries, want %d", len(list), tt.wantLength)
			}
		})
	}
}
