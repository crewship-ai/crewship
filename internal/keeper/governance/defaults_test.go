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

func TestUnconfiguredWorkspaceStartsFromInstanceDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	in := Settings{
		Enabled: true, DenyNotifyMinRisk: 4, WatchPresets: []string{"exfiltration"}, AutoLeaseSeconds: 900,
		BehaviorSampleEvery: 10, RequireSecondApprover: true,
		// Per-workspace references never become defaults: a user or a vault
		// credential belongs to one workspace.
		SecurityContactUserID: "u1", GovModelCredentialID: "cred-1",
	}
	if err := SetDefaults(ctx, db, in); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}

	got, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("found = true for a workspace with no row; the console must still say it inherits")
	}
	want := in
	want.SecurityContactUserID, want.GovModelCredentialID = "", ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %+v, want the defaults %+v", got, want)
	}
	if r := Resolve(ctx, db, nil, "ws1"); !reflect.DeepEqual(r, want) {
		t.Fatalf("Resolve = %+v, want the defaults %+v", r, want)
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
