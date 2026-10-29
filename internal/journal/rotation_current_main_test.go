package journal_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAuditRotationRetainsVerificationKeys(t *testing.T) {
	const oldSeed = "synthetic-audit-rotation-seed"
	t.Setenv("ENCRYPTION_KEY", oldSeed)
	t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", "")
	db := testutil.MigratedDB(t).DB
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('rotation-ws','Rotation','rotation-ws')`); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := journal.NewWriter(db, logger, journal.WriterOptions{FlushInterval: time.Hour, ChainKey: journal.DeriveChainKey(oldSeed)})
	entry := journal.Entry{WorkspaceID: "rotation-ws", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Before key rotation"}
	if _, err := first.EmitSync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	first.Close()
	oldKey := journal.DeriveChainKey(oldSeed)
	next := sha256.Sum256([]byte("synthetic-new-audit-key"))
	data, err := json.Marshal(map[string]any{"active_key_id": "audit-next", "keys": []map[string]string{{"id": "legacy-v1", "key_b64": base64.StdEncoding.EncodeToString(oldKey)}, {"id": "audit-next", "key_b64": base64.StdEncoding.EncodeToString(next[:])}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit-keys.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", path)
	t.Setenv("ENCRYPTION_KEY", "synthetic-changed-host-seed")
	current := journal.NewWriter(db, logger, journal.WriterOptions{FlushInterval: time.Hour})
	defer current.Close()
	entry.Summary = "After key rotation"
	if _, err := current.EmitSync(t.Context(), entry); err != nil {
		t.Fatalf("rotation blocked append: %v", err)
	}
	verified, err := journal.VerifyChain(t.Context(), db, "rotation-ws")
	if err != nil || !verified.OK {
		t.Fatalf("retained keys cannot verify chain: %+v %v", verified, err)
	}
}
