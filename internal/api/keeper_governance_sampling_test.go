package api

// keeper_governance_sampling_test.go — issue #1001 M3: the behaviour-monitor
// sampling rate as a workspace setting.
//
// The cadence existed in the hook (behaviorhook.SetSampleEvery) with no
// production caller and no way to reach it from config: every workspace ran on
// the hardwired "every 5th tool call" whatever it wanted. These tests pin the
// config half — the partial-update PUT round-trips the value, the bounds are
// enforced at the API boundary rather than clamped, and an aggressive cadence
// comes back with the advisory the response already carries for the four-eyes
// foot-gun.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// TestKeeperGovernance_BehaviorSampleEvery_RoundTrips is the core gap test: on
// main the field is not in the PUT body at all, so the write is silently dropped
// and the GET reports the unset sentinel.
func TestKeeperGovernance_BehaviorSampleEvery_RoundTrips(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut, `{"behavior_sample_every": 1}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeGovernance(t, rr.Body.Bytes()).BehaviorSampleEvery; got != 1 {
		t.Fatalf("PUT echoed behavior_sample_every = %d, want 1", got)
	}

	rr = doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.BehaviorSampleEvery != 1 {
		t.Fatalf("GET behavior_sample_every = %d, want 1 (the value the PUT stored)", res.BehaviorSampleEvery)
	}
}

// TestKeeperGovernance_BehaviorSampleEvery_UnsetIsSentinelZero: an existing row
// that predates this setting must keep the built-in cadence, not be pinned to
// whatever 5 happens to be today. 0 on the wire is "not set" — the effective
// value stays governance.DefaultBehaviorSampleEvery.
func TestKeeperGovernance_BehaviorSampleEvery_UnsetIsSentinelZero(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	// A write that says nothing about sampling.
	rr := doGovernanceReq(t, h, http.MethodPut, `{"enabled": true}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.BehaviorSampleEvery != 0 {
		t.Fatalf("behavior_sample_every = %d on a row that never set it, want 0 (unset)", res.BehaviorSampleEvery)
	}
	if eff := governance.EffectiveBehaviorSampleEvery(res.BehaviorSampleEvery); eff != governance.DefaultBehaviorSampleEvery {
		t.Fatalf("effective cadence for unset = %d, want the built-in default %d",
			eff, governance.DefaultBehaviorSampleEvery)
	}
}

// TestKeeperGovernance_BehaviorSampleEvery_Bounds. Rejected, not clamped, for
// the reason auto_lease_seconds is: a 200 with a quietly-rewritten number hides
// the fact that the operator asked for something the control cannot do.
//
// 0 is the one worth spelling out. SetSampleEvery(<=0) turns the hook into a
// no-op, so accepting 0 here would give a workspace that reads "watchdog
// enabled" while nothing is ever evaluated. Off is `crewship keeper disable`.
func TestKeeperGovernance_BehaviorSampleEvery_Bounds(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	cases := []struct {
		name    string
		value   int
		wantSub string
	}{
		{"zero is not off", 0, "keeper disable"},
		{"negative", -1, "between"},
		{"above the ceiling", governance.MaxBehaviorSampleEvery + 1, "between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"behavior_sample_every": %d}`, tc.value)
			rr := doGovernanceReq(t, h, http.MethodPut, body, wsID, userID)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("PUT %d status = %d, want 400; body=%s", tc.value, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantSub) {
				t.Errorf("error for %d = %s; want it to mention %q", tc.value, rr.Body.String(), tc.wantSub)
			}
		})
	}

	// The bounds themselves are accepted.
	for _, v := range []int{governance.MinBehaviorSampleEvery, governance.MaxBehaviorSampleEvery} {
		body := fmt.Sprintf(`{"behavior_sample_every": %d}`, v)
		rr := doGovernanceReq(t, h, http.MethodPut, body, wsID, userID)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT %d status = %d, want 200; body=%s", v, rr.Code, rr.Body.String())
		}
	}
}

// TestKeeperGovernance_BehaviorSampleEvery_WarnsOnAggressiveCadence: a cadence
// this tight puts a governance-model call behind (nearly) every tool call the
// workspace's agents make. That is a legitimate posture, so it is allowed — but
// it is also a bill, and the endpoint already has a non-blocking advisory
// channel for exactly this shape of foot-gun.
func TestKeeperGovernance_BehaviorSampleEvery_WarnsOnAggressiveCadence(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut, `{"behavior_sample_every": 1}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if w := decodeGovernance(t, rr.Body.Bytes()).Warning; w == "" {
		t.Fatal("behavior_sample_every=1 returned no warning; the cost of a judge call per tool call must be said out loud")
	}

	// The default cadence is not a foot-gun and must not nag.
	rr = doGovernanceReq(t, h, http.MethodPut,
		fmt.Sprintf(`{"behavior_sample_every": %d}`, governance.DefaultBehaviorSampleEvery), wsID, userID)
	if w := decodeGovernance(t, rr.Body.Bytes()).Warning; w != "" {
		t.Errorf("default cadence warned: %q", w)
	}
}

// TestKeeperGovernance_BehaviorSampleEvery_PartialUpdateKeepsIt proves the field
// obeys the partial-update contract in both directions: setting it leaves the
// rest of the row alone, and an unrelated edit does not reset it.
func TestKeeperGovernance_BehaviorSampleEvery_PartialUpdateKeepsIt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"enabled": true, "deny_notify_min_risk": 6, "behavior_sample_every": 12}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}

	// An unrelated single-field edit.
	rr = doGovernanceReq(t, h, http.MethodPut, `{"deny_notify_min_risk": 9}`, wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.BehaviorSampleEvery != 12 {
		t.Errorf("behavior_sample_every = %d after an unrelated edit, want 12", res.BehaviorSampleEvery)
	}
	if !res.Enabled || res.DenyNotifyMinRisk != 9 {
		t.Errorf("unrelated fields clobbered: %+v", res)
	}
}

// TestKeeperGovernance_BehaviorSampleEvery_JSONFieldName pins the wire name the
// CLI, the console and the OpenAPI schema all spell out by hand.
func TestKeeperGovernance_BehaviorSampleEvery_JSONFieldName(t *testing.T) {
	b, err := json.Marshal(governance.Settings{BehaviorSampleEvery: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"behavior_sample_every":7`) {
		t.Errorf("Settings JSON = %s; want a behavior_sample_every field", b)
	}
}
