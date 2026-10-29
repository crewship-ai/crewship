package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
)

func drainAlerts(t *testing.T) {
	t.Helper()
	if !waitForBackgroundWork(10 * time.Second) {
		t.Fatal("audit chain alert did not finish")
	}
}

// adminAlerts counts audit chain inbox items for user whose source starts
// with the incident key prefix.
func adminAlerts(t *testing.T, db *sql.DB, user, keyPrefix string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE target_user_id=? AND source_id LIKE ? AND json_extract(payload_json,'$.subkind')='audit_chain_incident'`, user, "audit_chain:"+keyPrefix+"%:"+user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func breakEvents(t *testing.T, db *sql.DB, entityPrefix string) (chained, fallback int) {
	t.Helper()
	if err := db.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE chain_seq IS NOT NULL),
		COUNT(*) FILTER (WHERE chain_seq IS NULL AND json_extract(metadata,'$.audit_chain')='unchained_fallback')
		FROM instance_audit_logs WHERE action='audit.chain_break_detected' AND entity_type='audit_chain' AND entity_id LIKE ?`, entityPrefix+"%").Scan(&chained, &fallback); err != nil {
		t.Fatal(err)
	}
	return chained, fallback
}

// A broken instance chain tail met by any audited action produces one
// incident event (unchained, since the chain itself is broken) and one inbox
// item per instance admin; repeats of the same incident add neither.
func TestAuditInstanceChainBreakAlertsAdmins(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	r, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	f.r = r
	breakInstanceAuditTail(t, f.db)
	if rr := f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/suspend", `{"reason":"incident response"}`); rr.Code != 200 {
		t.Fatalf("suspend with broken tail: HTTP %d %s", rr.Code, rr.Body.String())
	}
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:instance:"); chained != 0 || fallback != 1 {
		t.Fatalf("incident events chained=%d fallback=%d, want one fallback", chained, fallback)
	}
	if n := adminAlerts(t, f.db, "boss", "chain_break:instance:"); n != 1 {
		t.Fatalf("instance admin inbox items=%d, want 1", n)
	}
	if n := adminAlerts(t, f.db, "wsadmin", ""); n != 0 {
		t.Fatalf("non-instance-admin alerted: %d", n)
	}
	// Reactivation grants access: refused, and the same incident is not re-recorded.
	if rr := f.do(f.boss, "POST", "/api/v1/admin/instance/people/carol/reactivate", ``); rr.Code != 500 {
		t.Fatalf("reactivate with broken tail: HTTP %d", rr.Code)
	}
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:instance:"); chained+fallback != 1 {
		t.Fatalf("repeated incident recorded again: chained=%d fallback=%d", chained, fallback)
	}
}

// A workspace append refused by its chain tail alerts through the writer's
// chain-break observer, with a chained instance event.
func TestAuditWorkspaceChainBreakAlertsAdmins(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	r, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	for range 2 {
		if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-old", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "History"}); err != nil {
			t.Fatal(err)
		}
	}
	execOrFatal(t, f.db, `UPDATE journal_entries SET summary='changed' WHERE workspace_id='ws-old' AND seq=2`)
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-old", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Refused"}); err == nil {
		t.Fatal("append onto broken workspace tail succeeded")
	}
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:workspace:ws-old:seq:2"); chained != 1 || fallback != 0 {
		t.Fatalf("workspace incident events chained=%d fallback=%d, want one chained", chained, fallback)
	}
	if n := adminAlerts(t, f.db, "boss", "chain_break:workspace:ws-old:"); n != 1 {
		t.Fatalf("instance admin inbox items=%d, want 1", n)
	}
}

// Every reseal is visible in the instance trail and notifies instance admins.
func TestAuditResealAlertsAdmins(t *testing.T) {
	for _, scope := range []string{"instance", "workspace"} {
		t.Run(scope, func(t *testing.T) {
			f := newInstanceFixture(t)
			defer f.r.Shutdown()
			t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
			table, sequence := "instance_audit_logs", "chain_seq"
			body := map[string]any{"scope": scope, "reason": "Approved synthetic recovery"}
			writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
			defer writer.Close()
			f.r.journal = writer
			if scope == "workspace" {
				if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-old", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "History"}); err != nil {
					t.Fatal(err)
				}
				table, sequence = "journal_entries", "seq"
				body["workspace_id"] = "ws-old"
			} else if err := auditInstance(t.Context(), nil, f.db, "fixture.history", "instance", "", "", nil); err != nil {
				t.Fatal(err)
			}
			var seq int64
			var hash string
			if err := f.db.QueryRow(`SELECT `+sequence+`,entry_hash FROM `+table+` WHERE `+sequence+`>0 ORDER BY `+sequence+` DESC LIMIT 1`).Scan(&seq, &hash); err != nil {
				t.Fatal(err)
			}
			body["expected_tail_seq"], body["expected_tail_hash"] = seq, hash
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rr := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", string(raw))
			if rr.Code != 200 {
				t.Fatalf("reseal: HTTP %d %s", rr.Code, rr.Body.String())
			}
			drainAlerts(t)
			action := map[string]string{"instance": "audit.resealed", "workspace": "audit.workspace_resealed"}[scope]
			var events int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action=? AND chain_seq IS NOT NULL`, action).Scan(&events); err != nil || events != 1 {
				t.Fatalf("chained %s events=%d %v", action, events, err)
			}
			if n := adminAlerts(t, f.db, "boss", "reseal:"); n != 1 {
				t.Fatalf("reseal inbox items=%d, want 1", n)
			}
		})
	}
}

// On-demand verification that finds a defect behind an authentic tail (here
// post_upgrade_invalid) alerts too; appends never ran into it.
func TestAuditInstanceVerifyAlertsAdmins(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	f.r.journal = writer
	for range 3 {
		if err := auditInstance(t.Context(), nil, f.db, "fixture.history", "instance", "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	execOrFatal(t, f.db, `UPDATE instance_audit_logs SET metadata='{"tampered":true}' WHERE chain_seq=2`)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/audit/verify", "")
	var verdict struct {
		OK               bool   `json:"ok"`
		HistoricalStatus string `json:"historical_status"`
		BrokenSeq        int64  `json:"broken_seq"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &verdict); err != nil || rr.Code != 200 || verdict.OK || verdict.HistoricalStatus != "post_upgrade_invalid" || verdict.BrokenSeq != 2 {
		t.Fatalf("verify: HTTP %d %+v %v", rr.Code, verdict, err)
	}
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:instance:seq:2"); chained != 1 || fallback != 0 {
		t.Fatalf("incident events chained=%d fallback=%d, want one chained", chained, fallback)
	}
	var reason string
	if err := f.db.QueryRow(`SELECT json_extract(metadata,'$.reason') FROM instance_audit_logs WHERE action='audit.chain_break_detected'`).Scan(&reason); err != nil || reason[:len("post_upgrade_invalid")] != "post_upgrade_invalid" {
		t.Fatalf("incident reason %q %v", reason, err)
	}
	if n := adminAlerts(t, f.db, "boss", "chain_break:instance:seq:2"); n != 1 {
		t.Fatalf("instance admin inbox items=%d, want 1", n)
	}
}

// A committed incident is not proof of inbox delivery. A later full sweep
// retries missing per-admin notices after a delivery error, without duplicate
// inbox rows on subsequent sweeps.
func TestAuditIncidentRetriesFailedInboxDelivery(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	f.r.journal = writer
	for range 3 {
		if err := auditInstance(t.Context(), nil, f.db, "fixture.history", "instance", "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	execOrFatal(t, f.db, `UPDATE instance_audit_logs SET metadata='{"tampered":true}' WHERE chain_seq=2`)
	execOrFatal(t, f.db, `CREATE TRIGGER reject_audit_notice BEFORE INSERT ON inbox_items WHEN json_extract(NEW.payload_json,'$.subkind')='audit_chain_incident' BEGIN SELECT RAISE(ABORT,'synthetic delivery failure'); END`)
	if err := f.r.verifyAuditHistories(t.Context()); err != nil {
		t.Fatal(err)
	}
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:instance:seq:2"); chained+fallback != 1 {
		t.Fatalf("incident did not commit: %d/%d", chained, fallback)
	}
	if n := adminAlerts(t, f.db, "boss", ""); n != 0 {
		t.Fatalf("injected failure did not block delivery: %d", n)
	}
	execOrFatal(t, f.db, `DROP TRIGGER reject_audit_notice`)
	for range 2 {
		if err := f.r.verifyAuditHistories(t.Context()); err != nil {
			t.Fatal(err)
		}
		drainAlerts(t)
	}
	if n := adminAlerts(t, f.db, "boss", ""); n != 1 {
		t.Fatalf("delivery not retried/deduplicated: items=%d", n)
	}
}

func TestAuditNoticeReplayAfterDetectionIsGone(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	f.r.journal = writer
	inc := auditChainIncident{Kind: "chain_break", Scope: "instance", Seq: 42, Reason: "synthetic past incident"}
	if fresh, err := f.r.auditAlerts.record(t.Context(), inc); err != nil || !fresh {
		t.Fatalf("record: %v %v", fresh, err)
	}
	// No current defect, and no in-memory delivery state. A fresh sweep must
	// project the durable event, as after a restart in the commit/delivery gap.
	if err := f.r.verifyAuditHistories(t.Context()); err != nil {
		t.Fatal(err)
	}
	drainAlerts(t)
	if n := adminAlerts(t, f.db, "boss", inc.key()); n != 1 {
		t.Fatalf("missing replay: %d", n)
	}
}

func TestAuditAlertDoesNotReloadMissingBootKeys(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	f.r.auditAlerts.raise(journal.WithSigningKeys(t.Context(), nil), auditChainIncident{Kind: "chain_break", Scope: "instance", Seq: 42, Reason: "missing boot keys"})
	drainAlerts(t)
	if chained, fallback := breakEvents(t, f.db, "chain_break:instance:seq:42"); chained+fallback != 0 {
		t.Fatalf("nil boot snapshot reloaded environmental keys: %d/%d", chained, fallback)
	}
}

type auditNoticeCounter struct{ count atomic.Int64 }

func (n *auditNoticeCounter) NotifyInboxItem(_ context.Context, item inbox.Item) {
	if item.Payload["subkind"] == SubkindAuditChainIncident {
		n.count.Add(1)
	}
}

// The real purge boundary deletes inbox rows. A delivery receipt must outlive
// that projection, or every hourly replay creates another external alert.
func TestAuditNoticePurgeDoesNotResend(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	f.r.journal = writer
	counter := &auditNoticeCounter{}
	restore := inbox.SetExternalNotifierForTesting(counter)
	defer restore()
	inc := auditChainIncident{Kind: "chain_break", Scope: "instance", Seq: 42, Reason: "synthetic past incident"}
	if fresh, err := f.r.auditAlerts.record(t.Context(), inc); err != nil || !fresh {
		t.Fatalf("record: %v %v", fresh, err)
	}
	if err := f.r.verifyAuditHistories(t.Context()); err != nil {
		t.Fatal(err)
	}
	drainAlerts(t)
	if n := counter.count.Load(); n != 1 {
		t.Fatalf("first external notices=%d", n)
	}
	req := httptest.NewRequest("DELETE", "/api/v1/inbox?kind=message", nil)
	req.Header.Set("Authorization", "Bearer "+f.boss)
	req.Header.Set("X-Workspace-ID", "ws-old")
	rr := httptest.NewRecorder()
	f.r.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("purge: %d %s", rr.Code, rr.Body.String())
	}
	for range 2 {
		if err := f.r.verifyAuditHistories(t.Context()); err != nil {
			t.Fatal(err)
		}
		drainAlerts(t)
	}
	if n := adminAlerts(t, f.db, "boss", inc.key()); n != 0 {
		t.Fatalf("purged notice reappeared: %d", n)
	}
	if n := counter.count.Load(); n != 1 {
		t.Fatalf("external notice resent after purge: %d", n)
	}
}

func TestAuditNoticeReceiptFailureRollsBackInbox(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	inc := auditChainIncident{Kind: "chain_break", Scope: "instance", Seq: 42, Reason: "synthetic incident"}
	execOrFatal(t, f.db, `CREATE TRIGGER reject_audit_delivery_receipt BEFORE INSERT ON instance_audit_notice_deliveries BEGIN SELECT RAISE(ABORT,'synthetic receipt failure'); END`)
	f.r.auditAlerts.raise(t.Context(), inc)
	drainAlerts(t)
	if n := adminAlerts(t, f.db, "boss", inc.key()); n != 0 {
		t.Fatalf("inbox committed without delivery receipt: %d", n)
	}
	execOrFatal(t, f.db, `DROP TRIGGER reject_audit_delivery_receipt`)
	f.r.auditAlerts.raise(t.Context(), inc)
	drainAlerts(t)
	if n := adminAlerts(t, f.db, "boss", inc.key()); n != 1 {
		t.Fatalf("failed delivery was not retried: %d", n)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_notice_deliveries WHERE incident_key=? AND user_id='boss'`, inc.key()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("receipt count=%d: %v", n, err)
	}
}
