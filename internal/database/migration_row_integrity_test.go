package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestMalformedLegacyRowsRefuseInsteadOfDroppingHistory(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	for _, tc := range []struct {
		name, setup, table string
		run                func(context.Context, *sql.Tx, *slog.Logger) error
	}{
		{"webhook", `CREATE TABLE agents(id TEXT,webhook_secret TEXT);INSERT INTO agents VALUES(NULL,'inert-test-secret')`, "agents", migrationEncryptWebhookSecrets},
		{"memory timestamp", `CREATE TABLE memory_versions(id TEXT,written_at TEXT);INSERT INTO memory_versions VALUES(NULL,'2025-01-02 03:04:05')`, "memory_versions", migrationNormalizeMemoryVersionsTsformat},
		{"private services", `CREATE TABLE crews(id TEXT,services_json TEXT);INSERT INTO crews VALUES(NULL,'[]')`, "crews", migrationPrivateServices},
		{"notification preferences", `CREATE TABLE user_notification_prefs(id TEXT,workspace_id TEXT,user_id TEXT,category TEXT,channel_id TEXT,state TEXT);INSERT INTO user_notification_prefs VALUES(NULL,'w','u','system','c','off')`, "user_notification_prefs", remapPrefRows},
		{"notification channels", `CREATE TABLE notification_channels(id TEXT,categories_json TEXT);INSERT INTO notification_channels VALUES(NULL,'["system"]')`, "notification_channels", remapChannelAllowlists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := migrationBoundaryDB(t, tc.setup)
			rejectedMigration(t, db, tc.run, "Scan error")
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM ` + tc.table + ` WHERE id IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("malformed row was discarded: %d %v", count, err)
			}
		})
	}
}

func TestLegacyDataUpdatesFailAtomicallyWhenWritesAreRefused(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	for _, tc := range []struct {
		name, setup, table, column, want string
		run                              func(context.Context, *sql.Tx, *slog.Logger) error
	}{
		{"webhook", `CREATE TABLE agents(id TEXT,webhook_secret TEXT);INSERT INTO agents VALUES('id','inert-test-secret')`, "agents", "webhook_secret", "inert-test-secret", migrationEncryptWebhookSecrets},
		{"memory timestamp", `CREATE TABLE memory_versions(id TEXT,written_at TEXT);INSERT INTO memory_versions VALUES('id','2025-01-02 03:04:05')`, "memory_versions", "written_at", "2025-01-02 03:04:05", migrationNormalizeMemoryVersionsTsformat},
		{"private services", `CREATE TABLE crews(id TEXT,services_json TEXT);INSERT INTO crews VALUES('id','[{"name":"cache","image":"redis:7","command":["redis-server","--requirepass","inert-test-value"]}]')`, "crews", "services_json", `[{"name":"cache","image":"redis:7","command":["redis-server","--requirepass","inert-test-value"]}]`, migrationPrivateServices},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := migrationBoundaryDB(t, tc.setup, `CREATE TRIGGER refuse_update BEFORE UPDATE ON `+tc.table+` BEGIN SELECT RAISE(ABORT,'write refused');END`)
			rejectedMigration(t, db, tc.run, "write refused")
			var actual string
			if err := db.QueryRow(`SELECT ` + tc.column + ` FROM ` + tc.table + ` WHERE id='id'`).Scan(&actual); err != nil || actual != tc.want {
				t.Fatalf("refused update changed data: %q %v", actual, err)
			}
		})
	}
}

func TestBundledSkillSeedReportsUnreadableAndUnwritableStorage(t *testing.T) {
	for _, setup := range []string{`CREATE TABLE unrelated(id TEXT)`, `CREATE TABLE skills(id TEXT,name TEXT,slug TEXT,display_name TEXT,description TEXT,category TEXT,source TEXT,icon TEXT,content TEXT,verification TEXT,pricing_tier TEXT,version TEXT,tool_count INTEGER);CREATE TRIGGER refuse_skill BEFORE INSERT ON skills BEGIN SELECT RAISE(ABORT,'write refused');END`} {
		db := migrationBoundaryDB(t, setup)
		var logs strings.Builder
		if err := SeedBundledSkills(context.Background(), db, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
			t.Fatal(err)
		}
		if strings.Count(logs.String(), "level=ERROR") != 3 {
			t.Fatalf("each refused skill should be reported: %s", logs.String())
		}
	}
}

func TestTemplateSeedRefusalsAreReported(t *testing.T) {
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := SeedBuiltinTemplates(context.Background(), db, "w", logger); err == nil || !strings.Contains(err.Error(), "check builtin template") {
		t.Fatalf("missing workflow schema error=%v", err)
	}
	var logs strings.Builder
	if err := SeedBuiltinCrewTemplates(context.Background(), db, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "failed to update builtin crew template") {
		t.Fatal("missing crew schema was not reported")
	}
}
