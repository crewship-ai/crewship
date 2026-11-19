package cartographer

import (
	"context"
	"math"
	"testing"
)

// TestListClampsCallerLimit pins the allocation bound behind code-scanning
// alert #1071 (go/uncontrolled-allocation-size): List sized its result
// slice from the caller's limit, so List(..., math.MaxInt) panicked in
// makeslice before reading a row. The HTTP handler clamps ?limit= first;
// this pins the store's own bound for every other caller.
func TestListClampsCallerLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := Create(ctx, db, nil, Checkpoint{
			WorkspaceID:   "ws_test",
			MissionID:     "mis_1",
			JournalCursor: "cursor",
			Label:         "cp",
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	for _, limit := range []int{MaxListLimit + 1, math.MaxInt32, math.MaxInt} {
		list, err := List(ctx, db, "ws_test", "mis_1", limit)
		if err != nil {
			t.Fatalf("limit %d: list: %v", limit, err)
		}
		if len(list) != 2 {
			t.Fatalf("limit %d: want 2 rows, got %d", limit, len(list))
		}
		if cap(list) > MaxListLimit {
			t.Fatalf("limit %d: result capacity %d exceeds MaxListLimit %d", limit, cap(list), MaxListLimit)
		}
	}
}
