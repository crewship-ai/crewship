package secrets_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/keeper/secrets"
)

func setTestEncKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key))
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func seedUsers(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := "user-keeper-test"
	db.Exec(`INSERT OR IGNORE INTO users (id,email) VALUES (?,?)`, id, "keeper@test.com")
	return id
}

func seedWorkspace(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	wsID := "ws-keeper-test"
	db.Exec(`INSERT OR IGNORE INTO workspaces (id,name,slug) VALUES (?,?,?)`, wsID, "KeeperWS", "keeper-ws")
	db.Exec(`INSERT OR IGNORE INTO workspace_members (id,workspace_id,user_id,role) VALUES (?,?,?,?)`,
		"wm1", wsID, userID, "OWNER")
	return wsID
}

func insertCredential(t *testing.T, db *sql.DB, wsID, userID, name, typ string, level int, plainValue string) string {
	t.Helper()
	enc, err := encryption.Encrypt(plainValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	id := "cred-" + name
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO credentials (id,workspace_id,name,type,security_level,encrypted_value,created_by)
		 VALUES (?,?,?,?,?,?,?)`,
		id, wsID, name, typ, level, enc, userID)
	if err != nil {
		t.Fatalf("insert credential %s: %v", name, err)
	}
	return id
}

func TestStore_Reload_LoadsSecrets(t *testing.T) {
	setTestEncKey(t)
	db := openTestDB(t)
	userID := seedUsers(t, db)
	wsID := seedWorkspace(t, db, userID)

	insertCredential(t, db, wsID, userID, "ssh-key", "SECRET", 3, "BEGIN RSA PRIVATE KEY...")
	insertCredential(t, db, wsID, userID, "api-key", "API_KEY", 1, "sk-test-1234") // not a SECRET, should not load

	store := secrets.New()
	if err := store.Reload(context.Background(), db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if store.Count() != 1 {
		t.Errorf("expected 1 secret, got %d", store.Count())
	}

	cred, ok := store.Get("cred-ssh-key")
	if !ok {
		t.Fatal("expected to find cred-ssh-key")
	}
	if cred.PlainValue != "BEGIN RSA PRIVATE KEY..." {
		t.Errorf("unexpected plain value: %q", cred.PlainValue)
	}
	if cred.SecurityLevel != 3 {
		t.Errorf("expected security level 3, got %d", cred.SecurityLevel)
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	setTestEncKey(t)
	store := secrets.New()
	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("expected not found, got found")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	setTestEncKey(t)
	db := openTestDB(t)
	userID := seedUsers(t, db)
	wsID := seedWorkspace(t, db, userID)
	insertCredential(t, db, wsID, userID, "concurrent-cred", "SECRET", 1, "concval")

	store := secrets.New()
	if err := store.Reload(context.Background(), db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Get("cred-concurrent-cred")
			store.Count()
			store.All()
		}()
	}
	// Concurrent reload
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = store.Reload(context.Background(), db)
	}()

	wg.Wait()
}

func TestStore_Reload_SkipsDeletedCredentials(t *testing.T) {
	setTestEncKey(t)
	db := openTestDB(t)
	userID := seedUsers(t, db)
	wsID := seedWorkspace(t, db, userID)

	insertCredential(t, db, wsID, userID, "deleted-cred", "SECRET", 2, "should-not-load")
	db.ExecContext(context.Background(),
		`UPDATE credentials SET deleted_at = datetime('now') WHERE id = 'cred-deleted-cred'`)

	store := secrets.New()
	if err := store.Reload(context.Background(), db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if store.Count() != 0 {
		t.Errorf("expected 0 secrets (deleted), got %d", store.Count())
	}
}

// Ensure ENCRYPTION_KEY env var is not leaked by checking the key doesn't appear
// in any exported value.
func TestStore_EncryptionKeyNotLeaked(t *testing.T) {
	setTestEncKey(t)
	db := openTestDB(t)
	userID := seedUsers(t, db)
	wsID := seedWorkspace(t, db, userID)
	insertCredential(t, db, wsID, userID, "leak-check", "SECRET", 1, "mysecretvalue")

	store := secrets.New()
	if err := store.Reload(context.Background(), db); err != nil {
		t.Fatalf("reload: %v", err)
	}

	encKey := os.Getenv("ENCRYPTION_KEY")
	for _, c := range store.All() {
		if c.PlainValue == encKey {
			t.Error("encryption key leaked as plain value")
		}
	}
}
