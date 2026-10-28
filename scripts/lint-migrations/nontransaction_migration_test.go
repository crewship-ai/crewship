package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain_NonTransactionalMigrationBodyAndHelperChangesAreCaught(t *testing.T) {
	const registry = `package database
var migrations = []migration{
    {version: 20261028210747, name: "deck_native_family", fnNoTx: migrationDeckNativeKinds},
}
`
	const body = `package database
func migrationDeckNativeKinds(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
    return widenNativeKinds(ctx, db)
}
func widenNativeKinds(ctx context.Context, db *sql.DB) error {
    return db.exec("original schema mutation")
}
`
	for _, change := range []struct{ name, before, after string }{
		{"registered body", "return widenNativeKinds(ctx, db)", "return widenDifferentKinds(ctx, db)"},
		{"called helper", "original schema mutation", "changed shipped schema mutation"},
	} {
		t.Run(change.name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir, map[string]string{
				"internal/database/migrate.go":             registry,
				"internal/database/migrate_deck_native.go": body,
			})
			head := strings.Replace(body, change.before, change.after, 1)
			if head == body {
				t.Fatal("fixture must mutate the shipped migration")
			}
			if err := os.WriteFile(filepath.Join(dir, "internal", "database", "migrate_deck_native.go"), []byte(head), 0o644); err != nil {
				t.Fatal(err)
			}
			code, stderr := runLintHelper(t, dir)
			if code != 1 || !strings.Contains(stderr, "BODY CHANGED") || !strings.Contains(stderr, "v20261028210747") {
				t.Fatalf("shipped fnNoTx %s edit escaped guard: exit=%d stderr=%s", change.name, code, stderr)
			}
		})
	}
}
