package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestListSnapshots pins snapshot discovery + name parsing: only this DB's
// pre-migrate snapshots are returned, newest first, with from/to versions
// decoded from the filename.
func TestListSnapshots(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "crewship.db")
	if err := os.WriteFile(db, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Two snapshots for this DB + one unrelated file that must be ignored.
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("snap"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("crewship.db.pre-migrate-v130-to-v133-20260708T100000Z.bak")
	mk("crewship.db.pre-migrate-v133-to-v134-20260709T120000Z.bak")
	mk("crewship.db.backup") // not a pre-migrate snapshot → ignored

	snaps, err := ListSnapshots(db)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(snaps))
	}
	// Newest first.
	if snaps[0].FromVersion != 133 || snaps[0].ToVersion != 134 {
		t.Errorf("newest snapshot versions = v%d→v%d, want v133→v134", snaps[0].FromVersion, snaps[0].ToVersion)
	}
	if snaps[1].FromVersion != 130 {
		t.Errorf("older snapshot from-version = %d, want 130", snaps[1].FromVersion)
	}
}

// TestRestoreSnapshot_RoundTripWithGuard is the end-to-end downgrade story:
// migrate a DB, snapshot it, let a "newer Crewship" advance the schema past
// what this binary knows (which the version-skew guard from #912 then refuses
// to boot over), restore the snapshot, and confirm the guard passes again —
// i.e. the old binary can boot.
func TestRestoreSnapshot_RoundTripWithGuard(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crewship.db")
	ctx := context.Background()

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(ctx, db.DB, newTestLogger()); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	maxV := maxKnownMigrationVersion()

	// Snapshot the DB at its current (known-good) version, exactly as
	// SnapshotBeforeMigrate names them, then close so the file is consistent.
	snapPath := filepath.Join(dir, "crewship.db.pre-migrate-v"+itoa(maxV)+"-to-v"+itoa(maxV+1)+"-20260709T000000Z.bak")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapPath); err != nil {
		t.Fatalf("stage snapshot: %v", err)
	}

	// A newer Crewship migrated the live DB past what this binary knows.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO _migrations (version, name) VALUES (?, 'from_the_future')", maxV+1); err != nil {
		t.Fatalf("advance schema: %v", err)
	}
	db.Close()

	// The old binary now refuses to boot over the newer schema (guard).
	db2, _ := Open("file:" + dbPath)
	if err := Migrate(ctx, db2.DB, newTestLogger()); err == nil {
		t.Fatal("expected version-skew guard to refuse the advanced schema")
	}
	db2.Close()

	// Restore the snapshot, then the same binary boots cleanly.
	if err := RestoreSnapshot(dbPath, snapPath); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	db3, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open after restore: %v", err)
	}
	defer db3.Close()
	if err := Migrate(ctx, db3.DB, newTestLogger()); err != nil {
		t.Fatalf("migrate after restore should pass (schema back to v%d): %v", maxV, err)
	}
	var maxApplied int
	if err := db3.QueryRowContext(ctx, "SELECT MAX(version) FROM _migrations").Scan(&maxApplied); err != nil {
		t.Fatal(err)
	}
	if maxApplied != maxV {
		t.Errorf("restored schema max version = %d, want %d (the future row must be gone)", maxApplied, maxV)
	}
}

// TestRestoreSnapshot_RejectsForeignFile pins the safety guard: restore only
// accepts a pre-migrate snapshot belonging to the target DB, never an
// arbitrary path.
func TestRestoreSnapshot_RejectsForeignFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crewship.db")
	if err := os.WriteFile(dbPath, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "some-random.bak")
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSnapshot(dbPath, foreign); err == nil {
		t.Error("restore must reject a file that isn't a pre-migrate snapshot for this DB")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
