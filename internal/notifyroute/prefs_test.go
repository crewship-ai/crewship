package notifyroute

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
)

func TestPrefStore_SetAndGet_RoundTrips(t *testing.T) {
	db := newRouteTestDB(t)
	channels := notify.NewChannelStore(db)
	ch, err := channels.Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/x",
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	prefs := NewPrefStore(db)
	err = prefs.Set(context.Background(), "ws1", "u_member", []PrefCell{
		{Category: notify.CategoryApprovals, ChannelID: ch.ID, State: "immediate"},
		{Category: notify.CategorySecurity, ChannelID: ch.ID, State: "off"},
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := prefs.Get(context.Background(), "ws1", "u_member")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	idx := indexCells(got)
	if idx.state(notify.CategoryApprovals, ch.ID) != "immediate" {
		t.Errorf("approvals cell = %q, want immediate", idx.state(notify.CategoryApprovals, ch.ID))
	}
	if idx.state(notify.CategorySecurity, ch.ID) != "off" {
		t.Errorf("security cell = %q, want off", idx.state(notify.CategorySecurity, ch.ID))
	}
	// Unset cell defaults to off.
	if idx.state(notify.CategoryBudget, ch.ID) != "off" {
		t.Errorf("unset budget cell should default to off, got %q", idx.state(notify.CategoryBudget, ch.ID))
	}
}

func TestPrefStore_Set_UpsertsExistingCell(t *testing.T) {
	db := newRouteTestDB(t)
	channels := notify.NewChannelStore(db)
	ch, _ := channels.Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/x",
	})
	prefs := NewPrefStore(db)
	ctx := context.Background()

	if err := prefs.Set(ctx, "ws1", "u_member", []PrefCell{{Category: notify.CategoryBudget, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	if err := prefs.Set(ctx, "ws1", "u_member", []PrefCell{{Category: notify.CategoryBudget, ChannelID: ch.ID, State: "off"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := prefs.Get(ctx, "ws1", "u_member")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row after upsert (not 2), got %d", len(got))
	}
	if got[0].State != "off" {
		t.Errorf("state = %q, want off (second Set should overwrite)", got[0].State)
	}
}

func TestPrefStore_Set_RejectsUnknownCategory(t *testing.T) {
	db := newRouteTestDB(t)
	prefs := NewPrefStore(db)
	err := prefs.Set(context.Background(), "ws1", "u_member", []PrefCell{{Category: "not-a-category", ChannelID: "nch_x", State: "immediate"}})
	if err == nil {
		t.Fatal("expected rejection of an unknown category")
	}
}

func TestPrefStore_Set_RejectsDigestState(t *testing.T) {
	db := newRouteTestDB(t)
	prefs := NewPrefStore(db)
	// The DB CHECK admits 'digest' (schema is v2-ready) but the MVP store
	// layer rejects it — there is no digest scheduler to honor it yet.
	err := prefs.Set(context.Background(), "ws1", "u_member", []PrefCell{{Category: notify.CategorySystem, ChannelID: "nch_x", State: "digest"}})
	if err == nil {
		t.Fatal("expected rejection of state=digest at the store layer (v2 scope)")
	}
}

func TestPrefStore_MuteAllCell_RoundTrips(t *testing.T) {
	db := newRouteTestDB(t)
	channels := notify.NewChannelStore(db)
	ch, _ := channels.Create(context.Background(), notify.ChannelInput{
		WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.com/x",
	})
	prefs := NewPrefStore(db)
	ctx := context.Background()
	if err := prefs.Set(ctx, "ws1", "u_member", []PrefCell{{Category: notify.CategoryMuteAll, ChannelID: ch.ID, State: "immediate"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := prefs.Get(ctx, "ws1", "u_member")
	idx := indexCells(got)
	if !idx.muted(ch.ID) {
		t.Fatal("mute-all cell with state=immediate should report muted=true")
	}
}
