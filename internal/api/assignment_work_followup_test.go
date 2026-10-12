package api

import (
	"context"
	"testing"
)

func TestAssignmentFollowUpAdmissionRollsBackDeliveryClaimFailure(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatal(err)
	}
	seedFollowUpFixture(t, f, "receipt-session", "receipt-winner", "COMPLETED", 1)
	execOrFatal(t, f.db, `CREATE TRIGGER refuse_followup_claim BEFORE UPDATE OF state ON mission_comment_mentions WHEN NEW.state='claimed' BEGIN SELECT RAISE(ABORT,'delivery claim unavailable'); END`)
	if err := f.assign.dispatchQueuedFollowUpsForSession(ctx, "receipt-winner", f.wsID); err == nil {
		t.Fatal("delivery claim fault acknowledged")
	}
	var n int
	for _, q := range []string{`SELECT COUNT(*) FROM assignments WHERE session_id='receipt-session' AND id!='receipt-winner'`, `SELECT COUNT(*) FROM work_items WHERE source='assignment'`} {
		if err := f.db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial admission %s n=%d err=%v", q, n, err)
		}
	}
	execOrFatal(t, f.db, `DROP TRIGGER refuse_followup_claim`)
	for range 2 {
		if err := f.assign.dispatchQueuedFollowUpsForSession(ctx, "receipt-winner", f.wsID); err != nil {
			t.Fatal(err)
		}
	}
	f.assign.WaitDispatches()
	var claimed string
	if err := f.db.QueryRow(`SELECT a.id FROM assignments a JOIN work_items w ON w.domain_id=a.id JOIN mission_comment_mentions d ON d.claimed_by_run_id=a.id WHERE a.session_id='receipt-session' AND d.state='claimed'`).Scan(&claimed); err != nil {
		t.Fatalf("atomic input/delivery/work binding: %v", err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE session_id='receipt-session' AND id!='receipt-winner'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("replay domains=%d err=%v", n, err)
	}
}

// A dedicated connection is not a transaction: reject it before any domain
// write, so a future producer cannot accidentally autocommit an orphan.
func TestAssignmentAdmissionRejectsConnectionBeforeInsert(t *testing.T) {
	f := setupDelegationFixture(t)
	conn, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = insertCappedAssignment(t.Context(), conn, delegationScope{}, delegationLimits{}, dispatchCaller{}, cappedAssignment{})
	if err == nil {
		t.Fatal("non-transactional connection admitted")
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assignments`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial domain write: n=%d err=%v", n, err)
	}
}
