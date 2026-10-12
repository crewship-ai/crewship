package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCancelledMigrationsDoNotChangeExistingData(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	db := migrationBoundaryDB(t, `CREATE TABLE retained(value TEXT);INSERT INTO retained VALUES('original')`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func(context.Context, *sql.Tx, *slog.Logger) error{
		"legacy timestamps":       migrationBackfillLegacyTimestamps,
		"timestamp defaults":      migrationConvertDatetimeNowDefaults,
		"memory timestamp format": migrationNormalizeMemoryVersionsTsformat,
		"private services":        migrationPrivateServices,
		"network defaults":        migrateBackfillNetworkModeRestricted,
		"webhook secrets":         migrationEncryptWebhookSecrets,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			err = run(ctx, tx, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled migration error=%v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var value string
			if err := db.QueryRow(`SELECT value FROM retained`).Scan(&value); err != nil || value != "original" {
				t.Fatalf("data changed: %q %v", value, err)
			}
		})
	}
}

func TestCancelledLedgerOperationsRefuseBeforeWriting(t *testing.T) {
	db := migrationBoundaryDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, run := range map[string]func() error{
		"migrate":        func() error { return Migrate(ctx, db, logger) },
		"skip migration": func() error { return MigrateSkipping(ctx, db, logger, "missing") },
		"version skew":   func() error { return guardVersionSkew(ctx, db) },
		"read ledger":    func() error { _, err := ReadLedger(ctx, db); return err },
		"repair ledger": func() error {
			return ApplyLedgerRepair(ctx, db, RepairPlan{Renumbers: []Renumber{{Name: "move", From: 1, To: 2}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
	invoked := false
	if _, err := WithTxResult(ctx, db, func(*sql.Tx) (string, error) { invoked = true; return "wrong", nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("transaction result error=%v", err)
	}
	if invoked {
		t.Fatal("cancelled transaction invoked the writer")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='_migrations'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled operation created ledger: %d %v", count, err)
	}
}

func TestDataDirWithoutHomeReportsAnActionableError(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	for name, resolve := range map[string]func() (*DataDir, error){"provision": DefaultDataDir, "diagnostic": ResolveDefaultDataDir} {
		t.Run(name, func(t *testing.T) {
			dir, err := resolve()
			if err == nil || dir != nil || !strings.Contains(err.Error(), "home dir") {
				t.Fatalf("missing HOME accepted: %+v %v", dir, err)
			}
		})
	}
}

func TestDataDirProvisionRefusesAFileWithoutChangingIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(file, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDataDir(file); err == nil {
		t.Fatal("file accepted as directory")
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "preserved" {
		t.Fatalf("file changed: %q %v", got, err)
	}
}
