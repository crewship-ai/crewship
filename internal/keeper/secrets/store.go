// Package secrets provides an in-memory credential store for the Keeper security system.
// Credentials are loaded from the database at startup and kept decrypted in memory
// inside the crewshipd process — never in agent containers or env vars.
package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// DecryptedCredential holds a plaintext credential value with its metadata.
type DecryptedCredential struct {
	ID            string
	WorkspaceID   string
	Name          string
	Type          string
	SecurityLevel int
	KeeperCrewID  string
	PlainValue    string
}

// Store is a thread-safe in-memory credential store.
// Loaded at crewshipd startup; refreshed on credential changes.
type Store struct {
	mu      sync.RWMutex
	secrets map[string]DecryptedCredential // credential_id → plaintext
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		secrets: make(map[string]DecryptedCredential),
	}
}

// Reload fetches all SECRET-type credentials from the DB and decrypts them.
// It replaces the entire in-memory map atomically.
func (s *Store) Reload(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT id, workspace_id, name, type, security_level, COALESCE(keeper_crew_id,''), encrypted_value
		FROM credentials
		WHERE type = 'SECRET' AND status = 'ACTIVE' AND deleted_at IS NULL
		ORDER BY created_at ASC
	`)
	if err != nil {
		return fmt.Errorf("keeper secrets: query: %w", err)
	}
	defer rows.Close()

	next := make(map[string]DecryptedCredential)
	var decryptFailed int
	for rows.Next() {
		var c DecryptedCredential
		var encVal string
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Type,
			&c.SecurityLevel, &c.KeeperCrewID, &encVal); err != nil {
			return fmt.Errorf("keeper secrets: scan: %w", err)
		}
		plain, err := encryption.Decrypt(encVal)
		if err != nil {
			decryptFailed++
			continue
		}
		c.PlainValue = plain
		next[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("keeper secrets: rows: %w", err)
	}

	s.mu.Lock()
	s.secrets = next
	s.mu.Unlock()
	if decryptFailed > 0 {
		return fmt.Errorf("keeper secrets: %d credentials failed to decrypt", decryptFailed)
	}
	return nil
}

// Get returns the decrypted credential for credentialID, or false if not found.
func (s *Store) Get(credentialID string) (DecryptedCredential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.secrets[credentialID]
	return c, ok
}

// Count returns the number of loaded secrets.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.secrets)
}

// All returns a copy of all decrypted credentials (for audit/internal use).
func (s *Store) All() []DecryptedCredential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DecryptedCredential, 0, len(s.secrets))
	for _, c := range s.secrets {
		out = append(out, c)
	}
	return out
}
