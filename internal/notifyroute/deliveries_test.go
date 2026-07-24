package notifyroute

import (
	"context"
	"testing"
)

func TestDeliveryStore_InsertPending_CoalescesOnDedupKey(t *testing.T) {
	db := newRouteTestDB(t)
	deliveries := NewDeliveryStore(db)
	ctx := context.Background()

	d := Delivery{WorkspaceID: "ws1", ChannelID: "nch_1", UserID: "u_member", Category: "security", DedupKey: "security:src-1"}
	id1, created1, err := deliveries.InsertPending(ctx, d)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !created1 {
		t.Fatal("first insert should report created=true")
	}

	id2, created2, err := deliveries.InsertPending(ctx, d)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created2 {
		t.Fatal("re-firing the same (channel_id, dedup_key) should coalesce, not create a new row")
	}
	if id1 != id2 {
		t.Errorf("coalesced insert should return the ORIGINAL row's id: got %q want %q", id2, id1)
	}

	rows, err := deliveries.List(ctx, "ws1", ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 delivery row after coalescing, got %d", len(rows))
	}
}

func TestDeliveryStore_MarkSentAndFailed(t *testing.T) {
	db := newRouteTestDB(t)
	deliveries := NewDeliveryStore(db)
	ctx := context.Background()

	id, _, err := deliveries.InsertPending(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_1", Category: "security", DedupKey: "security:src-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := deliveries.MarkSent(ctx, id); err != nil {
		t.Fatal(err)
	}
	rows, _ := deliveries.List(ctx, "ws1", ListFilter{Status: StatusSent})
	if len(rows) != 1 || rows[0].Status != StatusSent {
		t.Fatalf("expected 1 sent row, got %+v", rows)
	}

	id2, _, err := deliveries.InsertPending(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_1", Category: "security", DedupKey: "security:src-3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := deliveries.MarkFailed(ctx, id2, "boom"); err != nil {
		t.Fatal(err)
	}
	rows2, _ := deliveries.List(ctx, "ws1", ListFilter{Status: StatusFailed})
	if len(rows2) != 1 || rows2[0].Error != "boom" {
		t.Fatalf("expected 1 failed row with error=boom, got %+v", rows2)
	}
}

func TestDeliveryStore_InsertDropped(t *testing.T) {
	db := newRouteTestDB(t)
	deliveries := NewDeliveryStore(db)
	ctx := context.Background()

	if err := deliveries.InsertDropped(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_1", Category: "chat.replies", DedupKey: "chat.replies:src-4"}, StatusDroppedRate); err != nil {
		t.Fatal(err)
	}
	rows, _ := deliveries.List(ctx, "ws1", ListFilter{Status: StatusDroppedRate})
	if len(rows) != 1 {
		t.Fatalf("expected 1 dropped_rate row, got %d", len(rows))
	}
}

func TestDeliveryStore_List_FiltersByChannelAndCategory(t *testing.T) {
	db := newRouteTestDB(t)
	deliveries := NewDeliveryStore(db)
	ctx := context.Background()

	deliveries.InsertPending(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_a", Category: "security", DedupKey: "k1"}) //nolint:errcheck
	deliveries.InsertPending(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_b", Category: "budget", DedupKey: "k2"})   //nolint:errcheck
	deliveries.InsertPending(ctx, Delivery{WorkspaceID: "ws1", ChannelID: "nch_a", Category: "budget", DedupKey: "k3"})   //nolint:errcheck

	byChannel, _ := deliveries.List(ctx, "ws1", ListFilter{ChannelID: "nch_a"})
	if len(byChannel) != 2 {
		t.Errorf("channel filter: got %d, want 2", len(byChannel))
	}
	byCategory, _ := deliveries.List(ctx, "ws1", ListFilter{Category: "budget"})
	if len(byCategory) != 2 {
		t.Errorf("category filter: got %d, want 2", len(byCategory))
	}
}
