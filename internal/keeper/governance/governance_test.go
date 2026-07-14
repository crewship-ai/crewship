package governance

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// FK targets for keeper_governance_settings.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w1')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'admin@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db.DB
}

func TestGetUnconfiguredWorkspace(t *testing.T) {
	db := openTestDB(t)
	s, found, err := Get(context.Background(), db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("expected found=false for unconfigured workspace")
	}
	if s.Enabled {
		t.Fatal("unconfigured settings must not report enabled")
	}
	if s.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("DenyNotifyMinRisk = %d, want default %d", s.DenyNotifyMinRisk, DefaultDenyNotifyMinRisk)
	}
}

func TestUpsertThenGetRoundTrips(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := Settings{Enabled: true, SecurityContactUserID: "u1", DenyNotifyMinRisk: 5}
	if err := Upsert(ctx, db, "ws1", in, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	s, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after Upsert")
	}
	if !reflect.DeepEqual(s, in) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", s, in)
	}

	// Second Upsert updates in place (PK conflict path) and clears the contact.
	in2 := Settings{Enabled: false, SecurityContactUserID: "", DenyNotifyMinRisk: 9}
	if err := Upsert(ctx, db, "ws1", in2, "u1"); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	s, _, err = Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !reflect.DeepEqual(s, in2) {
		t.Fatalf("update mismatch: got %+v, want %+v", s, in2)
	}
}

func TestUpsertRoundTripsWatchSpecAndPresets(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := Settings{
		Enabled:           true,
		DenyNotifyMinRisk: 7,
		WatchSpec:         "flag any read of ~/.ssh or id_rsa",
		WatchPresets:      []string{"credentials", "egress"},
	}
	if err := Upsert(ctx, db, "ws1", in, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !reflect.DeepEqual(s, in) {
		t.Fatalf("watch round-trip mismatch:\n got  %+v\n want %+v", s, in)
	}

	// Clearing the presets round-trips back to a nil slice (empty JSON default),
	// not an empty non-nil slice, so DeepEqual against a zero-value Settings holds.
	in2 := Settings{Enabled: true, DenyNotifyMinRisk: 7, WatchSpec: "", WatchPresets: nil}
	if err := Upsert(ctx, db, "ws1", in2, "u1"); err != nil {
		t.Fatalf("Upsert clear: %v", err)
	}
	s, _, err = Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if s.WatchSpec != "" || s.WatchPresets != nil {
		t.Fatalf("clear did not reset watch fields: %+v", s)
	}

	// Resolve surfaces the watch fields for a configured workspace.
	if err := Upsert(ctx, db, "ws1", in, "u1"); err != nil {
		t.Fatalf("Upsert restore: %v", err)
	}
	r := Resolve(ctx, db, nil, "ws1")
	if r.WatchSpec != in.WatchSpec || !reflect.DeepEqual(r.WatchPresets, in.WatchPresets) {
		t.Fatalf("Resolve dropped watch fields: %+v", r)
	}
}

func TestUpsertRoundTripsRequireSecondApprover(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := Settings{Enabled: true, DenyNotifyMinRisk: 7, RequireSecondApprover: true}
	if err := Upsert(ctx, db, "ws1", in, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !s.RequireSecondApprover {
		t.Fatalf("RequireSecondApprover did not round-trip: %+v", s)
	}

	// Flip back off — partial-update callers (the API handler) always send
	// the merged struct, so a plain re-Upsert must clear it.
	in2 := Settings{Enabled: true, DenyNotifyMinRisk: 7, RequireSecondApprover: false}
	if err := Upsert(ctx, db, "ws1", in2, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, _, err = Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if s.RequireSecondApprover {
		t.Fatalf("RequireSecondApprover did not clear: %+v", s)
	}
}

// Default OFF: an unconfigured workspace must never resolve to
// RequireSecondApprover=true — the four-eyes gate is opt-in.
func TestResolveDefaultsSecondApproverOff(t *testing.T) {
	db := openTestDB(t)
	if s := Resolve(context.Background(), db, nil, "ws1"); s.RequireSecondApprover {
		t.Fatal("unconfigured workspace must resolve RequireSecondApprover=false")
	}
}

func TestUpsertClampsRisk(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: true, DenyNotifyMinRisk: 42}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, _, _ := Get(ctx, db, "ws1")
	if s.DenyNotifyMinRisk != 10 {
		t.Fatalf("risk not clamped high: %d", s.DenyNotifyMinRisk)
	}
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: true, DenyNotifyMinRisk: -3}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, _, _ = Get(ctx, db, "ws1")
	if s.DenyNotifyMinRisk != 1 {
		t.Fatalf("risk not clamped low: %d", s.DenyNotifyMinRisk)
	}
}

func TestResolveDefaultsOffWithoutRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Opt-in default: an unconfigured workspace resolves to disabled with the
	// default DENY-notify threshold — no server inheritance.
	s := Resolve(ctx, db, nil, "ws1")
	if s.Enabled {
		t.Fatal("no row must resolve to disabled (opt-in default OFF)")
	}
	if s.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("DenyNotifyMinRisk = %d, want default %d", s.DenyNotifyMinRisk, DefaultDenyNotifyMinRisk)
	}

	// An explicit enabled row wins.
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: true, DenyNotifyMinRisk: 5}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s := Resolve(ctx, db, nil, "ws1"); !s.Enabled || s.DenyNotifyMinRisk != 5 {
		t.Fatalf("explicit enabled row not honored: %+v", s)
	}

	// An explicit disabled row also wins (stays off).
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: false, DenyNotifyMinRisk: 7}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s := Resolve(ctx, db, nil, "ws1"); s.Enabled {
		t.Fatal("explicit disabled row must resolve to disabled")
	}
}

func TestResolveSurvivesNilDBAndEmptyWorkspace(t *testing.T) {
	if s := Resolve(context.Background(), nil, nil, "ws1"); s.Enabled || s.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("nil db fallback wrong: %+v", s)
	}
	db := openTestDB(t)
	if s := Resolve(context.Background(), db, nil, ""); s.Enabled {
		t.Fatalf("empty workspace fallback wrong: %+v", s)
	}
}
