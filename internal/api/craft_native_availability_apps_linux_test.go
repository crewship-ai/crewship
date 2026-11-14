//go:build linux

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
)

// Availability follows the administrator's Word and Sheet settings (#5381):
// it never answers can_edit:true while lease/acquire would refuse with 503.
// A person who could otherwise edit learns why editing is off; reading stays.
func TestCraftNativeAvailabilityFollowsApplicationMode(t *testing.T) {
	type setting struct {
		desired craftapps.Desired
		drain   time.Duration
	}
	on := setting{craftapps.Enabled, 0}
	for _, tc := range []struct {
		name          string
		word, sheet   setting
		canEdit       bool
		refusal       string
		acquireStatus int
	}{
		{name: "word on", word: on, sheet: on, canEdit: true, acquireStatus: 200},
		{name: "word draining", word: setting{craftapps.Disabled, time.Hour}, sheet: on, refusal: "app_draining", acquireStatus: 503},
		{name: "word off", word: setting{craftapps.Disabled, 0}, sheet: on, refusal: "app_disabled", acquireStatus: 503},
		{name: "only sheet off", word: on, sheet: setting{craftapps.Disabled, 0}, canEdit: true, acquireStatus: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, router, call, owner, _, _ := craftNativeDataTCPFixture(t, true)
			now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
			policy := &craftapps.Policy{Now: func() time.Time { return now }}
			router.craftNativeHTTP.apps = newCraftAppsGate(policy, f.db)
			// A drain only follows an application that was on.
			for subject, s := range map[string]setting{craftapps.Word: tc.word, craftapps.Sheet: tc.sheet} {
				for _, step := range []setting{on, s} {
					if _, _, err := policy.Set(t.Context(), f.db, subject, step.desired, "admin", step.drain); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.refusal == "app_draining" && policy.Mode(t.Context(), f.db, craftapps.Word) != craftapps.ModeDraining {
				t.Fatal("fixture: Word is not draining")
			}
			status, raw := call("GET", "availability", owner, "", nil)
			var got struct {
				Enabled     bool   `json:"enabled"`
				CanEdit     bool   `json:"can_edit"`
				Agents      bool   `json:"agents_enabled"`
				Application string `json:"application"`
				Refusal     string `json:"edit_refusal"`
			}
			if status != 200 || json.Unmarshal(raw, &got) != nil {
				t.Fatalf("availability %d %s", status, raw)
			}
			if !got.Enabled || got.CanEdit != tc.canEdit || got.Refusal != tc.refusal || got.Application != "wordcraft" || !tc.canEdit && got.Agents {
				t.Fatalf("availability %s, want enabled can_edit=%v edit_refusal=%q application=wordcraft", raw, tc.canEdit, tc.refusal)
			}
			// The answer agrees with what the server does next.
			token, _ := craftNativeDataTCPAdmission(t, call, owner)
			if status, raw = call("POST", "lease", owner, token, struct{}{}); status != tc.acquireStatus {
				t.Fatalf("acquire %d %s, want %d", status, raw, tc.acquireStatus)
			}
			if status, raw = call("GET", "document", owner, token, nil); status != 200 {
				t.Fatalf("read %d %s", status, raw)
			}
		})
	}
}

// Someone without Edit gets no edit_refusal: the administrator is not why
// they cannot edit.
func TestCraftNativeAvailabilityRefusalNeedsEditAuthority(t *testing.T) {
	f, router, call, _, _, _ := craftNativeDataTCPFixture(t, true)
	policy := &craftapps.Policy{}
	router.craftNativeHTTP.apps = newCraftAppsGate(policy, f.db)
	reader := mintIngressScopedToken(t, f.db, f.adminID, "native-availability-reader", "pages:read")
	status, raw := call("GET", "availability", reader, "", nil)
	var got map[string]any
	if status != 200 || json.Unmarshal(raw, &got) != nil || got["can_edit"] != false || got["edit_refusal"] != nil {
		t.Fatalf("read-only availability %d %s", status, raw)
	}
}

// The drain is for the session that already holds the lease (#5381): it keeps
// renewing and saving, while a new session of the same person, which is what a
// reload opens, is refused custody like anyone else. Custody is bound to the
// session (craftNativePrincipal), so availability's can_edit:false matches
// what that reloaded page can do.
func TestCraftNativeDrainKeepsOnlyTheHoldingSession(t *testing.T) {
	f, router, call, owner, _, cfg := craftNativeDataTCPFixture(t, true)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	policy := &craftapps.Policy{Now: func() time.Time { return now }}
	router.craftNativeHTTP.apps = newCraftAppsGate(policy, f.db)
	if _, _, err := policy.Set(t.Context(), f.db, craftapps.Word, craftapps.Enabled, "admin", 0); err != nil {
		t.Fatal(err)
	}
	held, heldID := craftNativeDataTCPAdmission(t, call, owner)
	if status, raw := call("POST", "lease", owner, held, struct{}{}); status != 200 {
		t.Fatalf("acquire %d %s", status, raw)
	}
	if _, _, err := policy.Set(t.Context(), f.db, craftapps.Word, craftapps.Disabled, "admin", time.Hour); err != nil {
		t.Fatal(err)
	}
	if status, raw := call("POST", "lease/renew", owner, held, map[string]string{"generation": "1"}); status != 200 {
		t.Fatalf("holder renew while draining %d %s", status, raw)
	}
	save := craftNativeDataTCPBody(f, cfg, heldID, "1", "1", "opaque:drain:1", []byte(`{"v":2}`))
	if status, raw := call("POST", "document/save", owner, held, save); status != 200 {
		t.Fatalf("holder save while draining %d %s", status, raw)
	}
	reloaded, _ := craftNativeDataTCPAdmission(t, call, owner)
	status, raw := call("POST", "lease", owner, reloaded, struct{}{})
	var refusal struct{ Reason string }
	if status != 503 || json.Unmarshal(raw, &refusal) != nil || refusal.Reason != "app_draining" {
		t.Fatalf("reloaded session acquire %d %s, want 503 app_draining", status, raw)
	}
	if status, raw = call("GET", "document", owner, reloaded, nil); status != 200 {
		t.Fatalf("reloaded session read %d %s", status, raw)
	}
}
