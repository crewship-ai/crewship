package work

import (
	"context"
	"testing"
)

func TestRequestCancelTxSharesCommitAndRollback(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "starting"}[live], func(t *testing.T) {
			s, db, _ := newTestStore(t)
			ctx := context.Background()
			r := accept(t, s, db, backgroundReq("agent"))
			var claimed *Claimed
			if live {
				var err error
				claimed, err = s.Claim(ctx, ClaimOptions{LeaseOwner: "test"})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.MarkStarting(ctx, r.WorkID, claimed.RunID, claimed.Generation, "agent-run:"+claimed.RunID); err != nil {
					t.Fatal(err)
				}
			}
			before := countRows(t, db, "work_events")
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RequestCancelTx(ctx, tx, r.WorkID, "operator", "stop"); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			item, err := s.Get(ctx, r.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			if item.State == StateCancelled || countRows(t, db, "work_events") != before {
				t.Fatal("rollback leaked cancellation or history")
			}
			if live {
				if cancelled, err := s.CancelRequested(ctx, claimed.RunID); err != nil || cancelled {
					t.Fatalf("rollback request=%v err=%v", cancelled, err)
				}
			}
			for i := 0; i < 2; i++ {
				tx, err = db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.RequestCancelTx(ctx, tx, r.WorkID, "operator", "stop"); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if countRows(t, db, "work_events") != before+1 {
				t.Fatal("repeated cancellation duplicated history")
			}
			if live {
				if cancelled, err := s.CancelRequested(ctx, claimed.RunID); err != nil || !cancelled {
					t.Fatalf("committed request=%v err=%v", cancelled, err)
				}
				if err = s.MarkRuntimeRequested(ctx, r.WorkID, claimed.RunID, claimed.Generation); err == nil {
					t.Fatal("creation passed cancelled attempt")
				}
			} else {
				item, err = s.Get(ctx, r.WorkID)
				if err != nil || item.State != StateCancelled {
					t.Fatalf("state=%v err=%v", item, err)
				}
			}
		})
	}
}
