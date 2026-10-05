package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestSignalWaitDeadline_LegacyArmUsesCreatedAt(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()
	created := time.Now().Add(-2 * time.Hour)
	if _, err := db.Exec(`INSERT INTO pipeline_signal_waits(id,workspace_id,run_id,step_id,event_type,created_at) VALUES('legacy','ws','legacy-run','gate','approve',?)`, tsformat.Format(created)); err != nil {
		t.Fatal(err)
	}
	deadline, err := s.ArmWithTimeout(ctx, "ws", "legacy-run", "gate", "approve", time.Hour)
	if err != nil || !deadline.Equal(created.Add(time.Hour)) {
		t.Fatalf("legacy deadline=%s err=%v", deadline, err)
	}
	// A second recovery with a changed timeout must not renew the first one.
	again, err := s.ArmWithTimeout(ctx, "ws", "legacy-run", "gate", "approve", 3*time.Hour)
	if err != nil || !again.Equal(deadline) {
		t.Fatalf("renewed: %s %v", again, err)
	}
	if ok, err := s.Deliver(ctx, "legacy-run", "approve", "late"); ok || err != nil {
		t.Fatalf("late delivery=%v %v", ok, err)
	}
	if status, err := s.Resolve(ctx, "legacy-run", "gate"); status != "timed_out" || err != nil {
		t.Fatalf("resolve=%s %v", status, err)
	}
}

func TestSignalWaitDeadline_DeliveryAndExpiryArbitrate(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()
	for _, topic := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("topic=%v/expired=%v", topic, expired), func(t *testing.T) {
				for iteration := 0; iteration < 10; iteration++ {
					runID := fmt.Sprintf("race-%v-%v-%d", topic, expired, iteration)
					if _, err := s.ArmWithTimeout(ctx, "ws", runID, "gate", runID, time.Hour); err != nil {
						t.Fatal(err)
					}
					if expired {
						if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id=?`, tsformat.Format(time.Now().Add(-time.Minute)), runID); err != nil {
							t.Fatal(err)
						}
					}
					start := make(chan struct{})
					var wg sync.WaitGroup
					var accepted bool
					var deliverErr, expireErr error
					wg.Add(2)
					go func() {
						defer wg.Done()
						<-start
						if topic {
							var ids []string
							ids, deliverErr = s.DeliverTopic(ctx, "ws", runID, "payload")
							accepted = len(ids) == 1
						} else {
							accepted, deliverErr = s.Deliver(ctx, runID, runID, "payload")
						}
					}()
					go func() { defer wg.Done(); <-start; _, expireErr = s.Resolve(ctx, runID, "gate") }()
					close(start)
					wg.Wait()
					if deliverErr != nil || expireErr != nil {
						t.Fatalf("deliver=%v expire=%v", deliverErr, expireErr)
					}
					if accepted == expired {
						t.Fatalf("accepted=%v expired=%v", accepted, expired)
					}
					status, _ := waitStatus(t, db, runID)
					want := "delivered"
					if expired {
						want = "timed_out"
					}
					if status != want {
						t.Fatalf("status=%s want=%s", status, want)
					}
					// A valid committed signal survives expiry, and has only one consumer.
					if !expired {
						if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id=?`, tsformat.Format(time.Now().Add(-time.Minute)), runID); err != nil {
							t.Fatal(err)
						}
						if status, err := s.Resolve(ctx, runID, "gate"); err != nil || status != "delivered" {
							t.Fatalf("delivered signal expired: %s %v", status, err)
						}
						var claims int
						var mu sync.Mutex
						wg.Add(4)
						for j := 0; j < 4; j++ {
							go func() {
								defer wg.Done()
								payload, ok, err := s.ConsumeDelivered(ctx, runID, "gate")
								if err != nil {
									t.Error(err)
								}
								if ok {
									mu.Lock()
									claims++
									mu.Unlock()
									if payload != "payload" {
										t.Errorf("payload=%q", payload)
									}
								}
							}()
						}
						wg.Wait()
						if claims != 1 {
							t.Fatalf("consumers=%d", claims)
						}
					}
				}
			})
		}
	}
}
