//go:build linux

package api

import (
	"strings"
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
		{name: "edit reduced to comment", revoke: `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id='native-scope-writer'`, wantReason: "write_revoked", wantDraft: "retain"},
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

// The whole path a kept draft takes. Edit to Comment retires the stream and keeps
// the draft, a fresh native admission is refused, so no stream will ever speak
// again. What stays is draft-key: it must still say readable, and say revoked
// once Comment goes.
func TestCraftNativeCommentToNoneAfterAdmissionStopped(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	m = newCraftNativeHTTP(m.pages, true, clock)
	ctx := craftNativeGrantedWriter(t, f)
	token, _ := craftNativeIssued(t, m, ctx)
	if w := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatalf("acquire: %d %s", w.Code, w.Body)
	}
	next := craftNativeOpenStream(t, f, m, ctx, token)
	if event, _ := next(); event != "lease" {
		t.Fatalf("calibration stream: %s", event)
	}
	mustExec(t, f.db, `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
	for {
		event, value := next()
		if event == "lease" {
			continue
		}
		if event != "session_retired" || value["draft"] != "retain" || value["reason"] != "write_revoked" {
			t.Fatalf("retirement %s %v", event, value)
		}
		break
	}
	if w := craftNativeSessionCall(m, ctx); w.Code != 403 {
		t.Fatalf("fresh admission for Comment: %d %s, want 403", w.Code, w.Body)
	}
	if w := craftNativeDraftCheck(m, ctx, "native-scope-writer", "native-page"); w.Code != 200 || !strings.Contains(w.Body.String(), `"readable":true`) {
		t.Fatalf("Comment keeps Page read: %d %s", w.Code, w.Body)
	}
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
	if w := craftNativeDraftCheck(m, ctx, "native-scope-writer", "native-page"); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") {
		t.Fatalf("Comment removed: %d %s", w.Code, w.Body)
	}
}

// A person who reaches the Page only through a folder ACL depends on that lookup.
// A fault there says nothing about the person: the draft stays.
func TestCraftNativeRetirementKeepsTheDraftOnAnACLLookupFault(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	m = newCraftNativeHTTP(m.pages, true, clock)
	ctx := craftNativeGrantedWriter(t, f)
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('native-crew',?,'Crew','native-crew')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('native-folder',?,'native-folder','Folder','native-crew',datetime('now'),datetime('now'))`, f.wsID)
	mustExec(t, f.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('native-folder','user','native-scope-writer',1,0,datetime('now'))`)
	mustExec(t, f.db, `UPDATE pages SET folder_id='native-folder' WHERE id='native-page'`)
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
	ctx = craftNativeFreshContext(t, f, ctx, time.Hour)
	token, _ := craftNativeIssued(t, m, ctx)
	next := craftNativeOpenStream(t, f, m, ctx, token)
	if event, first := next(); event != "lease" {
		t.Fatalf("calibration stream: %s %v", event, first)
	}
	mustExec(t, f.db, `ALTER TABLE page_folder_acl RENAME TO page_folder_acl_unavailable`)
	for {
		event, value := next()
		if event == "lease" {
			continue
		}
		if event != "session_retired" || value["draft"] != "retain" {
			t.Fatalf("retirement %s %v, want draft retain", event, value)
		}
		return
	}
}
