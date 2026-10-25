package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (m *sessionCommitMode) recordFreshSaved(ctx context.Context, tx *sql.Tx, change BatchChange, saved BatchChangeResult) error {
	if m.recordSaved == nil {
		return nil
	}
	// Neither nested commands nor result slices handed to a trusted metadata
	// callback may alias the actual ledger, prepared output or final fences.
	raw, err := json.Marshal(struct {
		Change BatchChange
		Saved  BatchChangeResult
	}{change, saved})
	if err != nil {
		return err
	}
	var copied struct {
		Change BatchChange
		Saved  BatchChangeResult
	}
	if err := json.Unmarshal(raw, &copied); err != nil {
		return err
	}
	return m.recordSaved(ctx, tx, copied.Change, copied.Saved)
}
