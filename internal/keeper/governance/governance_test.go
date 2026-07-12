package governance

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
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
	if s != in {
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
	if s != in2 {
		t.Fatalf("update mismatch: got %+v, want %+v", s, in2)
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

func TestEffectiveInheritsServerDefaultWithoutRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if s := Effective(ctx, db, "ws1", true); !s.Enabled {
		t.Fatal("no row + serverDefault=true must be enabled")
	}
	if s := Effective(ctx, db, "ws1", false); s.Enabled {
		t.Fatal("no row + serverDefault=false must be disabled")
	}

	// Explicit row wins over the server default in both directions.
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: false, DenyNotifyMinRisk: 7}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s := Effective(ctx, db, "ws1", true); s.Enabled {
		t.Fatal("explicit disabled row must beat serverDefault=true")
	}
	if err := Upsert(ctx, db, "ws1", Settings{Enabled: true, DenyNotifyMinRisk: 7}, ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s := Effective(ctx, db, "ws1", false); !s.Enabled {
		t.Fatal("explicit enabled row must beat serverDefault=false")
	}
}

func TestEffectiveSurvivesNilDBAndEmptyWorkspace(t *testing.T) {
	if s := Effective(context.Background(), nil, "ws1", true); !s.Enabled || s.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("nil db fallback wrong: %+v", s)
	}
	db := openTestDB(t)
	if s := Effective(context.Background(), db, "", false); s.Enabled {
		t.Fatalf("empty workspace fallback wrong: %+v", s)
	}
}
