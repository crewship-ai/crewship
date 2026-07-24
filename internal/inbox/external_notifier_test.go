package inbox

import (
	"context"
	"sync"
	"testing"
)

// recordingNotifier is an ExternalNotifier test double that records every
// call, for asserting the Insert/UpsertMessage → notifyExternal chokepoint
// fires exactly when the design calls for (issue #1412).
type recordingNotifier struct {
	mu    sync.Mutex
	items []Item
}

func (r *recordingNotifier) NotifyInboxItem(_ context.Context, item Item) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
}

func (r *recordingNotifier) calls() []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Item, len(r.items))
	copy(out, r.items)
	return out
}

func TestInsert_FiresExternalNotifierOnNewRow(t *testing.T) {
	db := newInboxTestDB(t)
	rec := &recordingNotifier{}
	restore := SetExternalNotifierForTesting(rec)
	defer restore()

	if err := Insert(context.Background(), db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-ext-1",
		TargetUserID: "u1", Title: "Approve deploy",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 external-notify call, got %d", len(calls))
	}
	if calls[0].SourceID != "wp-ext-1" || calls[0].Kind != "waitpoint" {
		t.Errorf("unexpected item forwarded: %+v", calls[0])
	}
}

func TestInsert_DedupedRetryDoesNotRefireExternalNotifier(t *testing.T) {
	db := newInboxTestDB(t)
	rec := &recordingNotifier{}
	restore := SetExternalNotifierForTesting(rec)
	defer restore()

	item := Item{WorkspaceID: "ws1", Kind: "escalation", SourceID: "esc-ext-1", TargetRole: "MANAGER", Title: "Needs review"}
	if err := Insert(context.Background(), db, quietLogger(), item); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := Insert(context.Background(), db, quietLogger(), item); err != nil {
		t.Fatalf("retried insert: %v", err)
	}

	if got := len(rec.calls()); got != 1 {
		t.Fatalf("a deduped (kind, source_id) retry must not re-push a notification; got %d calls, want 1", got)
	}
}

func TestUpsertMessage_FiresExternalNotifierOnEveryCall(t *testing.T) {
	db := newInboxTestDB(t)
	rec := &recordingNotifier{}
	restore := SetExternalNotifierForTesting(rec)
	defer restore()

	item := Item{WorkspaceID: "ws1", Kind: KindMessage, SourceID: "chat_reply_c1_u1", TargetUserID: "u1", Title: "Agent replied"}
	if err := UpsertMessage(context.Background(), db, quietLogger(), item); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	item.BodyMD = "a second reply"
	if err := UpsertMessage(context.Background(), db, quietLogger(), item); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if got := len(rec.calls()); got != 2 {
		t.Fatalf("each UpsertMessage call (a genuinely new reply) should re-notify; got %d calls, want 2", got)
	}
}

func TestInsert_NoNotifierWiredIsSafeNoOp(t *testing.T) {
	db := newInboxTestDB(t)
	// No SetExternalNotifierForTesting call — externalNotifier is nil
	// (package default) unless a sibling test left it set; force nil
	// explicitly so this test is independent of run order.
	restore := SetExternalNotifierForTesting(nil)
	defer restore()

	if err := Insert(context.Background(), db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: "waitpoint", SourceID: "wp-ext-noop", Title: "x",
	}); err != nil {
		t.Fatalf("insert with no notifier wired should still succeed: %v", err)
	}
}
