package work

import (
	"testing"
	"time"
)

func TestHostChatFailureQueuesNoticeInSettlement(t *testing.T) {
	store, db, clock := newTestStore(t)
	clock.Advance(time.Since(clock.Now()) + time.Second)
	req := hostChatRequest("failure-notice")
	if _, _, err := store.BeginHostChat(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	exit := 1
	if err := store.FinishHostChat(t.Context(), req.RunID, HostChatResult{Status: "FAILED", ObservedExit: &exit}); err != nil {
		t.Fatal(err)
	}
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM run_failure_notice_outbox WHERE run_id=?`, req.RunID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("failed host run settled without a durable notice event: count=%d err=%v", count, err)
	}
}

func TestHostChatFailureNeverBackfillsOldAttempts(t *testing.T) {
	store, db, _ := newTestStore(t)
	req := hostChatRequest("pre-activation-failure")
	if _, _, err := store.BeginHostChat(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	exit := 1
	if err := store.FinishHostChat(t.Context(), req.RunID, HostChatResult{Status: "FAILED", ObservedExit: &exit}); err != nil {
		t.Fatal(err)
	}
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM run_failure_notice_outbox WHERE run_id=?`, req.RunID).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("pre-activation run queued an alert: count=%d err=%v", count, err)
	}
}
