package resourcelifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AcquireExistingIdentity verifies existing ownership for offline operations.
// It never creates, claims or rekeys an identity, even for a copied database.
func AcquireExistingIdentity(ctx context.Context, root string, db *sql.DB, location string) (*Identity, error) {
	if location == "" {
		return nil, fmt.Errorf("database location is required")
	}
	var nonce string
	if err := db.QueryRowContext(ctx, `SELECT db_nonce FROM resource_cleanup_installation WHERE id=1`).Scan(&nonce); err != nil {
		return nil, fmt.Errorf("read existing installation nonce: %w", err)
	}
	if !validHex(nonce) {
		return nil, fmt.Errorf("invalid database nonce")
	}
	dir := filepath.Join(root, "installations")
	sum := sha256.Sum256([]byte(location))
	expected := hex.EncodeToString(sum[:])
	owner, err := os.ReadFile(filepath.Join(dir, nonce+".db"))
	if err != nil {
		return nil, fmt.Errorf("read existing installation owner: %w", err)
	}
	if strings.TrimSpace(string(owner)) != expected {
		return nil, fmt.Errorf("installation identity belongs to another database location; reset refuses copied or moved databases")
	}
	bytes, err := os.ReadFile(filepath.Join(dir, nonce))
	if err != nil {
		return nil, fmt.Errorf("read existing installation identity: %w", err)
	}
	id := strings.TrimSpace(string(bytes))
	if !validHex(id) {
		return nil, fmt.Errorf("invalid installation identity")
	}
	lock, err := os.OpenFile(filepath.Join(dir, nonce+".lock"), os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open existing identity lock: %w", err)
	}
	held, err := tryExclusiveLock(lock)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrIdentityInUse
	}
	return &Identity{ID: id, lock: lock}, nil
}
