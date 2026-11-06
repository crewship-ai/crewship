//go:build linux

package api

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftNativeGrantedStream opens a real metadata stream for a grantee on a real
// clock and returns the first lease as calibration.
func craftNativeGrantedStream(t *testing.T, horizon time.Duration) (*pagesTransferRig, func() (string, map[string]any)) {
	t.Helper()
	f, m, _ := craftNativeHTTPFixture(t)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	m = newCraftNativeHTTP(m.pages, true, clock)
	ctx := craftNativeGrantedWriter(t, f)
	if horizon > 0 {
		ctx = craftNativeFreshContext(t, f, ctx, horizon)
	}
	token, _ := craftNativeIssued(t, m, ctx)
	if w := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatalf("calibration acquire: %d %s", w.Code, w.Body)
	}
	next := craftNativeOpenStream(t, f, m, ctx, token)
	if event, first := next(); event != "lease" || first["can_write"] != true {
		t.Fatalf("calibration stream: %s %v", event, first)
	}
	return f, next
}

// The retirement event is the only server statement that lets a browser erase a
// native draft. It must say "purge" for a proven loss of Page read and "retain"
// for a credential that merely ended: an ended sign-in proves nothing about the
// Page, as the draft-key route already answers (401, no draft_read_revoked).
func TestCraftNativeRetirementPurgesOnlyOnProvenReadLoss(t *testing.T) {
	for _, test := range []struct {
		name       string
		horizon    time.Duration
		revoke     string
		wantReason string
		wantDraft  string
	}{
		{name: "read grant removed", revoke: `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`, wantReason: "authority_changed", wantDraft: "purge"},
		{name: "workspace membership removed", revoke: `DELETE FROM workspace_members WHERE user_id='native-scope-writer'`, wantReason: "authority_changed", wantDraft: "purge"},
		{name: "page deleted", revoke: `DELETE FROM pages WHERE id='native-page'`, wantReason: "authority_changed", wantDraft: "purge"},
		{name: "write only removed", revoke: `UPDATE page_grants SET level='read' WHERE page_id='native-page' AND subject_id='native-scope-writer'`, wantReason: "write_revoked", wantDraft: "retain"},
		{name: "session revoked elsewhere", revoke: `UPDATE user_sessions SET revoked_at='2026-11-06T00:00:00Z' WHERE user_id='native-scope-writer'`, wantReason: "authority_changed", wantDraft: "retain"},
		{name: "session row removed", revoke: `DELETE FROM user_sessions WHERE user_id='native-scope-writer'`, wantReason: "authority_changed", wantDraft: "retain"},
		{name: "authority revision moved elsewhere", revoke: `UPDATE users SET authority_revision=authority_revision+1 WHERE id='native-scope-writer'`, wantReason: "authority_changed", wantDraft: "retain"},
		{name: "sign-in expired", horizon: 2 * time.Second, wantReason: "authority_changed", wantDraft: "retain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, next := craftNativeGrantedStream(t, test.horizon)
			if test.revoke != "" {
				mustExec(t, f.db, test.revoke)
			}
			for {
				event, value := next()
				if event == "lease" {
					continue
				}
				if event != "session_retired" || value["delivery"] != "stopped" || value["reason"] != test.wantReason || value["draft"] != test.wantDraft {
					t.Fatalf("retirement %s %v, want reason %s draft %s", event, value, test.wantReason, test.wantDraft)
				}
				return
			}
		})
	}
}

// Stage T0 -> T1 on a healthy stream. T0 is the committed revocation, T1 the
// session_retired frame read by the client over real TCP. The bound is one
// event interval plus delivery. CI load is why the margin is not tighter; the
// measured value is logged for the record.
func TestCraftNativeReadLossDeliveryWithinOneEventInterval(t *testing.T) {
	f, next := craftNativeGrantedStream(t, 0)
	committed := time.Now()
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
	for {
		event, value := next()
		if event == "lease" {
			continue
		}
		delivered := time.Since(committed)
		t.Logf("T0 committed -> T1 delivered: %s", delivered)
		if event != "session_retired" || value["draft"] != "purge" {
			t.Fatalf("delivery %s %v", event, value)
		}
		if limit := craftNativeEventInterval + 2*time.Second; delivered > limit {
			t.Fatalf("delivery took %s, bound %s", delivered, limit)
		}
		return
	}
}
