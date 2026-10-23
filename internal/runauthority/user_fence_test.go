package runauthority

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

func TestOriginalUserFenceSurvivesSerializedOriginAndRevokesQueuedRun(t *testing.T) {
	db, receipt := authorityFixture(t)
	// Origin storage carries provenance verbatim; credential liveness belongs
	// to the protected admission's exact-row check, not to JSON persistence.
	receipt.SourceCredentialKind = "cli_token"
	receipt.SourceCredentialID = "original-token"
	receipt.SourceCredentialBinding = "sha256:" + strings.Repeat("a", 64)
	putEntry(t, db, "start", "root", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureOrigin(t.Context(), tx, "root", "agent", "ws")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT human_authority FROM execution_origins WHERE id=?`, origin.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if json.Unmarshal([]byte(raw), &stored) != nil || stored["user_authority_revision"] != receipt.UserAuthorityRevision || receipt.UserAuthorityRevision == "" {
		t.Fatal("serialized origin lost original exact user fence", stored)
	}
	var roundTrip chataudience.Receipt
	if err := json.Unmarshal([]byte(raw), &roundTrip); err != nil || roundTrip != *receipt {
		t.Fatal("serialized origin replaced original credential provenance", roundTrip, err)
	}
	if _, err := db.Exec(`UPDATE users SET access_revoked_at='2026-10-23T21:00:00Z' WHERE id='human'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.Context(), db, "root", "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatal("queued run refreshed revoked original user", err)
	}
	if _, err := ValidateOriginReceipt(t.Context(), db, origin.ID, "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatal("serialized origin refreshed revoked original user", err)
	}
}
