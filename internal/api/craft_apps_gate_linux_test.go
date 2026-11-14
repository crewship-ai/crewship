//go:build linux

package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
)

// The administrator's Word setting over real HTTP (#5315): off refuses new
// editing but keeps reading; on allows it; a drain lets the open editor renew
// and save while nobody else may start; closing stops renewal and saving but
// release, reconcile and reading keep working. No document changes.
func TestCraftAppsGateOverTCP(t *testing.T) {
	f, router, call, owner, other, cfg := craftNativeDataTCPFixture(t, true)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	policy := &craftapps.Policy{Now: func() time.Time { return now }}
	router.craftNativeHTTP.apps = newCraftAppsGate(policy, f.db)
	set := func(d craftapps.Desired, drain time.Duration) {
		t.Helper()
		if _, _, err := policy.Set(t.Context(), f.db, craftapps.Word, d, "admin", drain); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(what string, status int, raw json.RawMessage, reason string) {
		t.Helper()
		var body struct{ Reason, Application string }
		if status != 503 || json.Unmarshal(raw, &body) != nil || body.Reason != reason || body.Application != "wordcraft" {
			t.Fatalf("%s: %d %s, want 503 %s", what, status, raw, reason)
		}
	}
	at, aid := craftNativeDataTCPAdmission(t, call, owner)
	bt, _ := craftNativeDataTCPAdmission(t, call, other)

	// Off (the default): reading works, editing does not start.
	status, raw := call("POST", "lease", owner, at, struct{}{})
	refused("acquire while off", status, raw, "app_disabled")
	if status, raw = call("GET", "document", owner, at, nil); status != 200 {
		t.Fatalf("read while off %d %s", status, raw)
	}
	status, raw = call("GET", "availability", owner, "", nil)
	if status != 200 || !strings.Contains(string(raw), `"wordcraft":"closed"`) {
		t.Fatalf("availability while off %d %s", status, raw)
	}

	set(craftapps.Enabled, 0)
	if status, raw = call("POST", "lease", owner, at, struct{}{}); status != 200 {
		t.Fatalf("acquire while on %d %s", status, raw)
	}
	save := craftNativeDataTCPBody(f, cfg, aid, "1", "1", "opaque:first:1", []byte(`{"v":2}`))
	if status, raw = call("POST", "document/save", owner, at, save); status != 200 {
		t.Fatalf("save while on %d %s", status, raw)
	}

	// Draining: the open editor continues, nobody new starts.
	set(craftapps.Disabled, time.Hour)
	if status, raw = call("POST", "lease/renew", owner, at, map[string]string{"generation": "1"}); status != 200 {
		t.Fatalf("renew while draining %d %s", status, raw)
	}
	second := craftNativeDataTCPBody(f, cfg, aid, "1", "2", "opaque:second:1", []byte(`{"v":3}`))
	if status, raw = call("POST", "document/save", owner, at, second); status != 200 {
		t.Fatalf("save while draining %d %s", status, raw)
	}
	status, raw = call("POST", "lease/request-handoff", other, bt, map[string]string{})
	refused("handoff request while draining", status, raw, "app_draining")
	status, raw = call("GET", "availability", owner, "", nil)
	if status != 200 || !strings.Contains(string(raw), `"wordcraft":"draining"`) {
		t.Fatalf("availability while draining %d %s", status, raw)
	}

	// Closed: no renewal and no save; reconcile, release and read still work.
	now = now.Add(2 * time.Hour)
	status, raw = call("POST", "lease/renew", owner, at, map[string]string{"generation": "1"})
	refused("renew after the drain", status, raw, "app_disabled")
	third := craftNativeDataTCPBody(f, cfg, aid, "1", "3", "opaque:third:1", []byte(`{"v":4}`))
	status, raw = call("POST", "document/save", owner, at, third)
	refused("save after the drain", status, raw, "app_disabled")
	if status, raw = call("POST", "document/reconcile", owner, at, second); status != 200 {
		t.Fatalf("reconcile while closed %d %s", status, raw)
	}
	if status, raw = call("POST", "lease/release", owner, at, map[string]string{"generation": "1"}); status != 200 {
		t.Fatalf("release while closed %d %s", status, raw)
	}
	status, raw = call("GET", "document", owner, at, nil)
	if status != 200 || !strings.Contains(string(raw), `"version":"3"`) {
		t.Fatalf("read while closed %d %s", status, raw)
	}
}
