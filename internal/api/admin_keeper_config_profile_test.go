package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// The judge profile has to ride the config endpoint with the same provenance the
// wiring has. A console that could show the values but not who decided them
// would offer a Reset with no visible referent.
func TestAdminKeeperConfig_ProfileIsServedWithProvenance(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keeperEnvDefaults)

	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/config", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	p := decodeKeeperConfig(t, rr).Profile
	if p.Name.Value != string(keepercfg.DefaultProfile) || p.Name.Source != "default" {
		t.Errorf("name = %q/%s, want %q/default", p.Name.Value, p.Name.Source, keepercfg.DefaultProfile)
	}
	if !p.HardGate.Value || p.HardGate.Source != "default" {
		t.Errorf("hard_gate = %v/%s, want true/default", p.HardGate.Value, p.HardGate.Source)
	}
	if len(p.Choices) != len(keepercfg.Profiles) || len(p.Facts) != len(keepercfg.EvidenceFacts) {
		t.Errorf("choices/facts = %v/%v — a picker must not have to hardcode either", p.Choices, p.Facts)
	}
	if p.Stamp == "" {
		t.Error("stamp is empty; it is what the decision record is compared against")
	}
}

// null means "follow the profile", false means "off". Collapsing the two here
// would make an operator undoing a change pin the capability off instead.
func TestAdminKeeperConfig_PutToggleTriState(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keeperEnvDefaults)

	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config",
		`{"judge_profile":"thorough","judge_precedent":false}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	p := decodeKeeperConfig(t, rr).Profile
	if p.Precedent.Value || p.Precedent.Source != "instance" {
		t.Errorf("precedent = %v/%s, want false/instance", p.Precedent.Value, p.Precedent.Source)
	}
	if p.ConsistencySamples.Value != 3 || p.ConsistencySamples.Source != "profile" {
		t.Errorf("consistency_samples = %d/%s, want 3/profile",
			p.ConsistencySamples.Value, p.ConsistencySamples.Source)
	}

	rr = httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", `{"judge_precedent":null}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	p = decodeKeeperConfig(t, rr).Profile
	if !p.Precedent.Value || p.Precedent.Source != "profile" {
		t.Errorf("precedent = %v/%s after null, want true/profile", p.Precedent.Value, p.Precedent.Source)
	}
}

func TestAdminKeeperConfig_PutRejectsBadProfileInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"unknown profile", `{"judge_profile":"aggressive"}`, http.StatusBadRequest},
		{"unknown fact", `{"judge_evidence_facts":"crew_scope"}`, http.StatusBadRequest},
		{"even sample count", `{"judge_consistency_samples":2}`, http.StatusBadRequest},
		{"toggle is not tri-state", `{"judge_hard_gate":"maybe"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newKeeperConfigHandler(t, keeperEnvDefaults)
			rr := httptest.NewRecorder()
			h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", tc.body))
			if rr.Code != tc.code {
				t.Errorf("got %d, want %d: %s", rr.Code, tc.code, rr.Body.String())
			}
		})
	}
}
