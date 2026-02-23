package server

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/keeper/secrets"
)

// secretsAdapter adapts *secrets.Store to the api.SecretGetter interface,
// which expects Get(credentialID string) (plainValue string, found bool).
type secretsAdapter struct {
	store *secrets.Store
}

func (a *secretsAdapter) Get(credentialID string) (string, bool) {
	cred, found := a.store.Get(credentialID)
	return cred.PlainValue, found
}

// newSecretsAdapter creates a secrets store, loads all SECRET credentials from
// the database, and returns an adapter that satisfies api.SecretGetter.
// Returns nil if the store cannot be loaded (credentials may not be decryptable
// without a valid ENCRYPTION_KEY, which is acceptable in dev/test environments).
func newSecretsAdapter(ctx context.Context, db *sql.DB, logger *slog.Logger) *secretsAdapter {
	store := secrets.New()
	if err := store.Reload(ctx, db); err != nil {
		logger.Warn("keeper: failed to load secrets store — /keeper/execute ALLOW will return 500",
			"error", err)
		return nil
	}
	count := store.Count()
	logger.Info("keeper secrets store loaded", "secret_credentials", count)
	return &secretsAdapter{store: store}
}
