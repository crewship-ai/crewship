package database

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestPrivateServicesMigrationAndRestore(t *testing.T) {
	setV140TestKey(t)
	db := v140FreshDB(t)
	mustExec(t, db.DB, `INSERT INTO workspaces(id,name,slug) VALUES('private-ws','Private','private-ws')`)
	const raw = `[{"name":"cache","image":"redis:7","command":["redis-server","--requirepass","inert-private-canary"]}]`
	mustExec(t, db.DB, `INSERT INTO crews(id,workspace_id,name,slug,services_json,deleted_at) VALUES('private-crew','private-ws','Private','private-crew',?,'2026-09-01')`, raw)
	hook := RestoreBackfillFor(20260908142800)
	if hook == nil {
		t.Fatal("legacy backup restoration needs the encryption hook")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var prior string
	for attempt := 0; attempt < 2; attempt++ {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := hook(context.Background(), tx, logger); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := db.QueryRow("SELECT services_json FROM crews WHERE id='private-crew'").Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored, "inert-private-canary") {
			t.Fatal("plaintext survived migration")
		}
		if opened, err := serviceconfig.Open(stored); err != nil || opened != raw {
			t.Fatal("migration changed service settings")
		}
		if attempt == 1 && stored != prior {
			t.Fatal("restore hook must be idempotent")
		}
		prior = stored
	}
	mustExec(t, db.DB, `UPDATE crews SET services_json=? WHERE id='private-crew'`, raw)
	t.Setenv("ENCRYPTION_KEY", "")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := hook(context.Background(), tx, logger); err == nil {
		t.Fatal("keyless migration must not be marked successful")
	}
}
