package governance

import (
	"context"
	"reflect"
	"testing"
)

// Instance defaults are what an instance admin saved for "All workspaces":
// every workspace got an explicit row then, and a workspace created later has
// none — it must start from those values, not from the built-in opt-out.

func TestDefaultsAbsentMeansBuiltIn(t *testing.T) {
	db := openTestDB(t)
	d, found, err := Defaults(context.Background(), db)
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if found {
		t.Fatal("found = true on a fresh instance")
	}
	if want := (Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}); !reflect.DeepEqual(d, want) {
		t.Fatalf("Defaults = %+v, want the built-in %+v", d, want)
	}
}

// The defaults are a template, not a live policy: a workspace takes a copy
// when it is created (SeedWorkspace) and keeps it. A workspace with no row of
// its own reads the built-in opt-out, never whatever the defaults are today —
// otherwise changing the defaults would silently change it.
func TestAWorkspaceWithoutARowDoesNotFollowTheDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := SetDefaults(ctx, db, Settings{Enabled: true, DenyNotifyMinRisk: 4}); err != nil {
		t.Fatal(err)
	}
	got, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if found || got.Enabled || got.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("Get = %+v (found %v), want the built-in opt-out", got, found)
	}
}

func TestSeedWorkspaceTakesACopyOfTheDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	in := Settings{
		Enabled: true, DenyNotifyMinRisk: 4, WatchPresets: []string{"exfiltration"}, AutoLeaseSeconds: 900,
		BehaviorSampleEvery: 10, RequireSecondApprover: true, GovModelProvider: "ollama", GovModelID: "qwen",
	}
	if err := SetDefaults(ctx, db, in); err != nil {
		t.Fatal(err)
	}
	if err := SeedWorkspace(ctx, db, "ws1"); err != nil {
		t.Fatalf("SeedWorkspace: %v", err)
	}
	got, found, _ := Get(ctx, db, "ws1")
	if !found || !reflect.DeepEqual(got, in) {
		t.Fatalf("seeded = %+v (found %v), want the defaults %+v", got, found, in)
	}
	// Changing the defaults afterwards leaves the workspace alone.
	if err := SetDefaults(ctx, db, Settings{Enabled: false, DenyNotifyMinRisk: 9}); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := Get(ctx, db, "ws1"); !reflect.DeepEqual(after, in) {
		t.Fatalf("after a defaults change the workspace reads %+v, want its own copy %+v", after, in)
	}
}

func TestSeedWorkspaceWithoutDefaultsWritesTheBuiltIn(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := SeedWorkspace(ctx, db, "ws1"); err != nil {
		t.Fatal(err)
	}
	got, found, _ := Get(ctx, db, "ws1")
	if !found || got.Enabled || got.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("seeded = %+v (found %v), want an explicit built-in row", got, found)
	}
}

func TestSeedWorkspaceNeverOverwritesARow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	own := Settings{Enabled: true, DenyNotifyMinRisk: 2}
	if err := Upsert(ctx, db, "ws1", own, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaults(ctx, db, Settings{DenyNotifyMinRisk: 9}); err != nil {
		t.Fatal(err)
	}
	if err := SeedWorkspace(ctx, db, "ws1"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := Get(ctx, db, "ws1"); !reflect.DeepEqual(got, own) {
		t.Fatalf("Get = %+v, want the workspace's own %+v kept", got, own)
	}
}

func TestExplicitRowWinsOverDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := SetDefaults(ctx, db, Settings{Enabled: true, DenyNotifyMinRisk: 3}); err != nil {
		t.Fatal(err)
	}
	own := Settings{Enabled: false, DenyNotifyMinRisk: 9}
	if err := Upsert(ctx, db, "ws1", own, "u1"); err != nil {
		t.Fatal(err)
	}
	got, found, _ := Get(ctx, db, "ws1")
	if !found || !reflect.DeepEqual(got, own) {
		t.Fatalf("Get = %+v (found %v), want the workspace's own %+v", got, found, own)
	}
}

func TestSetDefaultsClampsLikeUpsert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := SetDefaults(ctx, db, Settings{DenyNotifyMinRisk: 40, AutoLeaseSeconds: 5, BehaviorSampleEvery: 1000}); err != nil {
		t.Fatal(err)
	}
	d, _, _ := Defaults(ctx, db)
	if d.DenyNotifyMinRisk != 10 || d.AutoLeaseSeconds != MinAutoLeaseSeconds || d.BehaviorSampleEvery != MaxBehaviorSampleEvery {
		t.Fatalf("Defaults = %+v, want the same clamps a workspace row gets", d)
	}
}

func TestUpsertInsideARolledBackTransactionWritesNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Upsert(ctx, tx, "ws1", Settings{Enabled: true, DenyNotifyMinRisk: 5}, "u1"); err != nil {
		t.Fatalf("Upsert in tx: %v", err)
	}
	if err := SetDefaults(ctx, tx, Settings{Enabled: true, DenyNotifyMinRisk: 5}); err != nil {
		t.Fatalf("SetDefaults in tx: %v", err)
	}
	_ = tx.Rollback()
	if _, found, _ := Get(ctx, db, "ws1"); found {
		t.Fatal("row survived the rollback")
	}
	if _, found, _ := Defaults(ctx, db); found {
		t.Fatal("defaults survived the rollback")
	}
}
