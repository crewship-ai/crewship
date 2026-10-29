package mooring

import (
	"context"
	"database/sql"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// FullCheckpointSnapshot contains retained interpretation and target bookkeeping,
// never participant authority. ReadFullCheckpoint uses the store's immutable
// current-reader callback; there is no caller-supplied authorization override.
type FullCheckpointSnapshot struct {
	Checkpoint protocol.Checkpoint
	Targets    []BatchTargetVersion
}

func (s *BatchStore) ReadFullCheckpoint(ctx context.Context, lease OwnerLease) (FullCheckpointSnapshot, error) {
	var result FullCheckpointSnapshot
	if s == nil || s.ready() != nil || !s.checkpointIdentityEnabled() || !s.targetsEnabled() {
		return result, ErrCheckpointIdentity
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if e := s.config.Owners.Check(ctx, tx, lease); e != nil {
			return e
		}
		if e := s.authorize(ctx, tx, lease.Key, "read_checkpoint", nil, "", ""); e != nil {
			return e
		}
		var local bool
		var head string
		if e := tx.QueryRowContext(ctx, `SELECT workspace_id=? AND application_id=?,CASE WHEN length(CAST(revision AS BLOB))<=20 THEN revision ELSE '' END FROM mooring_collab_documents WHERE page_id=?`, lease.Key.WorkspaceID, lease.Key.ApplicationID, lease.Key.ArtifactID).Scan(&local, &head); e != nil {
			return e
		}
		revision, e := batchCounter(head)
		if e != nil || !local {
			return ErrCheckpointIdentity
		}
		cp, _, e := s.loadRetainedFullCheckpoint(ctx, tx, lease.Key, head, true)
		if e != nil {
			return e
		}
		targets, e := initialSessionTargets(ctx, tx, s, lease.Key, revision)
		if e != nil {
			return e
		}
		// Expensive decoding, metadata validation and SQL trigger work cannot turn
		// an earlier reader/owner decision into a perpetual startup capability.
		if e = s.authorize(ctx, tx, lease.Key, "read_checkpoint", nil, "", ""); e != nil {
			return e
		}
		if e = s.config.Owners.Check(ctx, tx, lease); e != nil {
			return e
		}
		result = FullCheckpointSnapshot{Checkpoint: cp, Targets: targets}
		return nil
	})
	if err != nil {
		return FullCheckpointSnapshot{}, err
	}
	return result, nil
}
