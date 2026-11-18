package notifyroute

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestRunFailureNoticeOutboxSurvivesExternalFailure(t *testing.T) {
	for _, change := range []string{"retry", "revoked"} {
		t.Run(change, func(t *testing.T) { checkRunFailureNoticeRetry(t, change == "revoked") })
	}
}

func checkRunFailureNoticeRetry(t *testing.T, revoke bool) {
	t.Helper()
	db := newRouteTestDB(t)
	r := newTestRouter(db, nil, nil)
	var sends atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sends.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	ch, err := r.channels.Create(t.Context(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: target.URL, Scope: notify.ScopeUser, OwnerUserID: "u_member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.prefs.Set(t.Context(), "ws1", "u_member", []PrefCell{
		{Category: notify.CategoryAgentsError, ChannelID: ch.ID, State: "immediate"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('notice-crew','ws1','Crew','notice')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('notice-agent','ws1','notice-crew','Lead','lead')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('notice-chat','ws1','notice-agent','u_member','private')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	store := work.NewStore(db)
	id := "failure-notice-run"
	if _, _, err := store.BeginHostChat(t.Context(), work.HostChatRequest{
		RunID: id, WorkspaceID: "ws1", CrewID: "notice-crew", AgentID: "notice-agent",
		ChatID: "notice-chat", UserID: "u_member", TriggerMessageID: "trigger-message",
	}); err != nil {
		t.Fatal(err)
	}
	exit := 1
	sensitive := "sensitive prompt diagnostic"
	if err := store.FinishHostChat(t.Context(), id, work.HostChatResult{Status: "FAILED", ObservedExit: &exit, Result: work.RunResult{ErrorMessage: &sensitive}}); err != nil {
		t.Fatal(err)
	}
	if err := r.drainRunFailureNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	var user, body, payload string
	if err := db.QueryRow(`SELECT target_user_id,body_md,payload_json FROM inbox_items WHERE kind='failed_run' AND source_id=?`, id).Scan(&user, &body, &payload); err != nil {
		t.Fatal(err)
	}
	if user != "u_member" || strings.Contains(body+payload, sensitive) {
		t.Fatalf("unexpected audience or disclosure: user=%s body=%s payload=%s", user, body, payload)
	}
	if rows, err := r.deliveries.List(t.Context(), "ws1", ListFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("private work delivery in workspace audit: count=%d err=%v", len(rows), err)
	}
	var deliveryID string
	if err := db.QueryRow(`SELECT id FROM notification_deliveries WHERE source_id=? AND status='failed'`, id).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE notification_deliveries SET updated_at='2020-01-01T00:00:00Z' WHERE id=?`, deliveryID); err != nil {
		t.Fatal(err)
	}
	restarted := newTestRouter(db, nil, nil)
	if err := restarted.drainRunFailureNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	if revoke {
		if _, err := db.Exec(`UPDATE workspace_members SET access_mode='restricted' WHERE user_id='u_member'`); err != nil {
			t.Fatal(err)
		}
		if attempted, sent := restarted.RecoverStuckDeliveries(t.Context()); attempted != 1 || sent != 0 || sends.Load() != 1 {
			t.Fatalf("revoked delivery attempted=%d sent=%d total sends=%d", attempted, sent, sends.Load())
		}
		if status, _ := deliveryStatus(t, restarted, deliveryID); status != StatusDroppedPref {
			t.Fatalf("revoked delivery remains eligible: %q", status)
		}
		return
	}
	if attempted, sent := restarted.RecoverStuckDeliveries(t.Context()); attempted != 1 || sent != 1 || sends.Load() != 2 {
		t.Fatalf("recovery attempted=%d sent=%d total sends=%d", attempted, sent, sends.Load())
	}
	if status, _ := deliveryStatus(t, restarted, deliveryID); status != StatusSent {
		t.Fatalf("delivery status=%q", status)
	}
	var watermark int64
	if err := db.QueryRow(`SELECT watermark FROM run_failure_notice_state WHERE id=1`).Scan(&watermark); err != nil || watermark == 0 {
		t.Fatalf("watermark=%d err=%v", watermark, err)
	}
	if _, err := db.Exec(`DELETE FROM inbox_items WHERE source_id=?`, id); err != nil {
		t.Fatal(err)
	}
	// Model a drain that read the event before the first writer committed.
	if err := restarted.projectRunFailureNotice(t.Context(), watermark, id); err != nil {
		t.Fatal(err)
	}
	var cards int
	if err := db.QueryRow(`SELECT count(*) FROM inbox_items WHERE source_id=?`, id).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("stale outbox reader recreated dismissed card: count=%d err=%v", cards, err)
	}
}

func TestRunFailureNoticeRecoveryPlansIndexedRange(t *testing.T) {
	db := newRouteTestDB(t)
	rows, err := db.Query("EXPLAIN QUERY PLAN "+pendingRunFailureNotices, runFailureNoticeBatch)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sawRange := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SEARCH run_failure_notice_outbox USING INTEGER PRIMARY KEY (rowid>?)") {
			sawRange = true
		}
		if strings.Contains(detail, "SCAN ") {
			t.Errorf("unexpected scan: %s", detail)
		}
	}
	if err := rows.Err(); err != nil || !sawRange {
		t.Fatalf("expected indexed recovery range, saw=%v err=%v", sawRange, err)
	}
}

func TestRunFailureNoticeForInterruptedLeadAssignment(t *testing.T) {
	db := newRouteTestDB(t)
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('notice-crew','ws1','Crew','notice')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('notice-lead','ws1','notice-crew','Lead','lead','LEAD'),('notice-worker','ws1','notice-crew','Worker','worker','AGENT')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('notice-chat','ws1','notice-worker','u_member','private')`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_by_user_id)
		  VALUES('notice-assignment','ws1','notice-chat','notice-worker','notice-lead','fixture','PENDING','u_member')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	accepted, err := work.AcceptAssignmentTx(ctx, tx, "notice-assignment")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := work.NewStore(db)
	claimed, err := store.Claim(ctx, work.ClaimOptions{LeaseOwner: "fixture-worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStarting(ctx, accepted.WorkID, claimed.RunID, claimed.Generation, "agent-run:"+claimed.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRunning(ctx, accepted.WorkID, claimed.RunID, claimed.Generation, "agent-run:"+claimed.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, work.TransitionRequest{
		WorkID: accepted.WorkID, RunID: claimed.RunID, Generation: claimed.Generation,
		To: work.StateNeedsReconciliation, Reason: "runtime stopped unexpectedly",
		ExitEvidence: "provider_exit:137", StoppedRunFailed: true,
	}); err != nil {
		t.Fatal(err)
	}
	// A new router drains the durable receipt even without a journal wakeup.
	if err := newTestRouter(db, nil, nil).drainRunFailureNotices(ctx); err != nil {
		t.Fatal(err)
	}
	var user string
	if err := db.QueryRow(`SELECT target_user_id FROM inbox_items WHERE kind='failed_run' AND source_id=?`, claimed.RunID).Scan(&user); err != nil || user != "u_member" {
		t.Fatalf("lead assignment notice recipient=%q err=%v", user, err)
	}
}
