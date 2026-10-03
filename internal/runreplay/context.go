// Package runreplay owns host-side state required to safely project retained
// execution output. It does not grant credential access to workloads.
package runreplay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

const maxContextBytes = 1 << 20
const contextPurpose = "crewship.run-replay-scrub.v1"

var ErrContextUnavailable = errors.New("run replay scrub context unavailable")
var ErrContextConflict = errors.New("run replay scrub context identity conflict")

type ContextStore struct{ db *sql.DB }

func NewContextStore(db *sql.DB) *ContextStore { return &ContextStore{db: db} }

type scrubContext struct {
	Purpose     string   `json:"purpose"`
	WorkspaceID string   `json:"workspace_id"`
	RunID       string   `json:"run_id"`
	Values      []string `json:"values"`
}

// Save preserves the original literal secrets for scrubbing, not for granting
// access. Existing run identities cannot be reassigned or overwritten. This
// path always encrypts, even if legacy plaintext-secret opt-out is enabled.
// Callers must retain the context until output projection is acknowledged and
// supply the lifecycle cleanup; this store does not expire unacknowledged data.
func (s *ContextStore) Save(ctx context.Context, workspaceID, runID string, values []string) error {
	if workspaceID == "" || runID == "" || len(workspaceID) > 256 || len(runID) > 256 {
		return ErrContextUnavailable
	}
	values = normalizeValues(values)
	doc := scrubContext{Purpose: contextPurpose, WorkspaceID: workspaceID, RunID: runID, Values: values}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > maxContextBytes {
		return ErrContextUnavailable
	}
	envelope, err := encryption.Encrypt(string(data))
	if err != nil {
		return ErrContextUnavailable
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO run_replay_contexts(id,workspace_id,secret_values_enc,created_at)
		VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, runID, workspaceID, envelope, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return ErrContextUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrContextUnavailable
	}
	if n == 1 {
		return nil
	}
	prior, err := s.Load(ctx, workspaceID, runID)
	if err != nil || !slices.Equal(prior, values) {
		return ErrContextConflict
	}
	return nil
}

// Load requires both durable scope identifiers. The encrypted document binds
// the same identifiers, so copying ciphertext to another row does not change
// its authority. Plaintext and unversioned legacy values are not accepted.
func (s *ContextStore) Load(ctx context.Context, workspaceID, runID string) ([]string, error) {
	if workspaceID == "" || runID == "" {
		return nil, ErrContextUnavailable
	}
	var envelope string
	if err := s.db.QueryRowContext(ctx, `SELECT secret_values_enc FROM run_replay_contexts WHERE id=? AND workspace_id=?`, runID, workspaceID).Scan(&envelope); err != nil {
		return nil, ErrContextUnavailable
	}
	if len(envelope) > 2*maxContextBytes || !encryption.IsEncrypted(envelope) {
		return nil, ErrContextUnavailable
	}
	plaintext, err := encryption.Decrypt(envelope)
	if err != nil || len(plaintext) > maxContextBytes {
		return nil, ErrContextUnavailable
	}
	var doc scrubContext
	if err = json.Unmarshal([]byte(plaintext), &doc); err != nil || doc.Purpose != contextPurpose || doc.WorkspaceID != workspaceID || doc.RunID != runID {
		return nil, ErrContextUnavailable
	}
	return doc.Values, nil
}

func normalizeValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}
