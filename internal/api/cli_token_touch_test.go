package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func readCLITokenLastUsed(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var lastUsed sql.NullString
	if err := db.QueryRow(`SELECT last_used_at FROM cli_tokens WHERE id = ?`, id).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	return lastUsed
}

// #4770: the last_used_at breadcrumb used to be written by a goroutine racing
// the handler, so the pool opened a second SQLite connection (a full schema
// parse) per test database. It is now written before validation returns, on
// the caller's connection, and at most once per interval per token.
func TestValidateCLITokenTouchesLastUsedOnTheCallerAndThrottles(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	id := "touch-" + generateCUID()
	token := cliTokenStandardPrefix + "touch" + id
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES (?, ?, 'touch', ?, 'STANDARD', datetime('now'))`, id, userID, hashStandard(token))

	if _, err := ValidateCLITokenFull(context.Background(), db, token, ValidateAuditContext{}); err != nil {
		t.Fatal(err)
	}
	// No waiting: the write happened before ValidateCLITokenFull returned.
	if got := readCLITokenLastUsed(t, db, id); !got.Valid {
		t.Fatal("last_used_at not written before validation returned")
	}
	if open := db.Stats().OpenConnections; open != 1 {
		t.Fatalf("validation opened %d connections, want the caller's one", open)
	}

	// Within the interval a second use does not write again.
	execOrFatal(t, db, `UPDATE cli_tokens SET last_used_at = 'sentinel' WHERE id = ?`, id)
	if _, err := ValidateCLITokenFull(context.Background(), db, token, ValidateAuditContext{}); err != nil {
		t.Fatal(err)
	}
	if got := readCLITokenLastUsed(t, db, id); got.String != "sentinel" {
		t.Fatalf("throttled use rewrote last_used_at: %q", got.String)
	}
}

// A failed write must not hide the breadcrumb for a whole interval: a token
// used once while the lock was busy would otherwise read as never used.
func TestCLITokenTouchFailureRetriesSoon(t *testing.T) {
	id := "touch-fail-" + generateCUID()
	closed := testutil.MigratedSQLDB(t)
	_ = closed.Close()
	touchCLITokenLastUsed(context.Background(), closed, id, "now")
	cliTokenTouchMu.Lock()
	next := cliTokenTouched[id]
	cliTokenTouchMu.Unlock()
	if wait := time.Until(next); wait <= 0 || wait > cliTokenTouchRetry {
		t.Fatalf("failed touch retries in %v, want within %v", wait, cliTokenTouchRetry)
	}
}
