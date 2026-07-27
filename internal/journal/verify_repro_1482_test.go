package journal

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestVerifyChain_Repro1482_ConcurrentEmit reproduces the shape stage produces:
// mission.status_change entries emitted from many goroutines at once, the way
// the seed drives them, then a full chain verify.
//
// #1482: stage is reseeded with --nuke on the current binary every run, yet
// `journal verify` reports 306 content-hash breaks out of ~137k entries, all
// kind="content", starting on consecutive mission.status_change rows one
// millisecond apart. dev3 with the same binary is clean. Emit is
// self-consistent by construction — it hashes and stores the same variables —
// so a break has to come from either a post-write mutation or from two writers
// racing the chain head.
func TestVerifyChain_Repro1482_ConcurrentEmit(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef") //gitleaks:allow — fake test fixture key

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: 5 * time.Millisecond})
	defer w.Close()

	ctx := context.Background()
	const workers, perWorker = 8, 60

	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Same shape as issue_handler.go's mission status writer.
				_, _ = w.Emit(ctx, Entry{
					WorkspaceID: "ws_test",
					CrewID:      "crew_1",
					MissionID:   fmt.Sprintf("m_%d_%d", g, i),
					Type:        EntryMissionStatus,
					Severity:    SeverityInfo,
					ActorType:   ActorUser,
					ActorID:     "user_1",
					Summary:     "status_changed: BACKLOG → TODO",
					Payload:     map[string]any{"action": "status_changed", "details": "BACKLOG → TODO"},
					Refs:        map[string]any{"mission_id": fmt.Sprintf("m_%d_%d", g, i), "activity_id": ""},
				})
			}
		}(g)
	}
	wg.Wait()

	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK {
		t.Errorf("chain broken after %d concurrent emits: count=%d break_count=%d first_seq=%d reason=%s",
			workers*perWorker, res.Count, res.BreakCount, res.BrokenSeq, res.Reason)
		for i, b := range res.Breaks {
			if i >= 5 {
				break
			}
			t.Logf("  break seq=%d kind=%s id=%s", b.Seq, b.Kind, b.ID)
		}
	}
	if want := workers * perWorker; res.Count != want {
		t.Errorf("count = %d, want %d — entries were dropped, not just mis-chained", res.Count, want)
	}
}
