package runreplay

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	_ "modernc.org/sqlite"
)

var testKey = strings.Repeat("0123456789abcdef", 4) // Synthetic encryption fixture, never a provider credential.

func testContextStore(t *testing.T) (*ContextStore, *sql.DB) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", testKey)
	t.Setenv(encryption.KeyVersionEnvVar, "v1")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE workspaces(id TEXT PRIMARY KEY); INSERT INTO workspaces VALUES('w1'),('w2');`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261003120810_run_replay_contexts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return NewContextStore(db), db
}

func TestScrubContextEncryptedAndBoundToRun(t *testing.T) {
	store, db := testContextStore(t)
	secret := "synthetic-original-token-for-scrubbing"
	if err := store.Save(t.Context(), "w1", "r1", []string{secret, secret}); err != nil {
		t.Fatal(err)
	}
	var envelope string
	if err := db.QueryRow(`SELECT secret_values_enc FROM run_replay_contexts WHERE id='r1'`).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if !encryption.IsEncrypted(envelope) || strings.Contains(envelope, secret) {
		t.Fatal("context stored without encryption")
	}
	values, err := store.Load(t.Context(), "w1", "r1")
	if err != nil || len(values) != 1 || values[0] != secret {
		t.Fatal("original scrub literal not restored")
	}
	if _, err = store.Load(t.Context(), "w2", "r1"); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("cross-workspace context disclosed")
	}
	if _, err = db.Exec(`INSERT INTO run_replay_contexts VALUES('r2','w2',?,'2026-10-03T12:00:00Z')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(t.Context(), "w2", "r2"); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("copied ciphertext changed scope")
	}
	if err = store.Save(t.Context(), "w1", "r1", []string{"replacement"}); !errors.Is(err, ErrContextConflict) {
		t.Fatal("original context overwritten")
	}
	if err = store.Save(t.Context(), "w1", "r1", []string{secret}); err != nil {
		t.Fatal("identical retry refused")
	}
}

func TestScrubContextRefusesPlaintextOptOut(t *testing.T) {
	store, db := testContextStore(t)
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	if err := store.Save(t.Context(), "w1", "r1", []string{"synthetic-secret"}); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("missing encryption key accepted")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_replay_contexts`).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed encryption left context")
	}
}

func TestScrubContextSurvivesVersionedKeyRotation(t *testing.T) {
	store, db := testContextStore(t)
	if err := store.Save(t.Context(), "w1", "r1", []string{"before-rotation"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY_V2", strings.Repeat("ab", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "v2")
	if err := store.Save(t.Context(), "w1", "r2", []string{"after-rotation"}); err != nil {
		t.Fatal(err)
	}
	values, err := store.Load(t.Context(), "w1", "r1")
	if err != nil || len(values) != 1 || values[0] != "before-rotation" {
		t.Fatal("old key version no longer readable")
	}
	var envelope string
	if err = db.QueryRow(`SELECT secret_values_enc FROM run_replay_contexts WHERE id='r2'`).Scan(&envelope); err != nil || !strings.HasPrefix(envelope, "v2:") {
		t.Fatal("new context did not use current key")
	}
	found := false
	for _, column := range encryption.EnvelopeColumns {
		if column.Table == "run_replay_contexts" && column.Column == "secret_values_enc" {
			found = true
		}
	}
	if !found {
		t.Fatal("rotation/backup inventory omits retained contexts")
	}
}
