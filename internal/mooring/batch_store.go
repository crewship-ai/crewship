package mooring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
)

// BatchDenied is a trusted host authorization decision. Other callback errors
// abort without a terminal receipt, since storage/authority outcomes may be unknown.
type BatchDenied struct{ Code string }

func (d *BatchDenied) Error() string { return d.Code }
func (s *BatchStore) authorize(ctx context.Context, tx *sql.Tx, k SessionKey, intent string, op *BatchOperation, task, participant string) error {
	if op != nil {
		isolated := cloneBatchOperation(*op)
		op = &isolated
	}
	if err := s.config.Authority(ctx, tx, BatchAccess{store: s, Intent: intent, Scope: k, Operation: op, TaskID: task, Participant: participant}); err != nil {
		return err
	}
	return requireLegacyCollaborationProfile(ctx, tx, k)
}
func (s *BatchStore) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ownerFence(ctx, tx); err != nil {
		return err
	}
	var sync int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		return err
	}
	if sync < 2 {
		return ErrInvalid
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func batchScopeMatches(ctx context.Context, tx *sql.Tx, k SessionKey) (string, []byte, error) {
	if err := requireLegacyCollaborationProfile(ctx, tx, k); err != nil {
		return "", nil, err
	}
	if err := ownerScopeExists(ctx, tx, k); err != nil {
		return "", nil, err
	}
	var workspace, app, revision, encoded string
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,application_id,revision,checkpoint_base64 FROM mooring_collab_documents WHERE page_id=?`, k.ArtifactID).Scan(&workspace, &app, &revision, &encoded)
	if err != nil {
		return "", nil, err
	}
	if workspace != k.WorkspaceID || app != k.ApplicationID {
		return "", nil, ErrInvalid
	}
	if _, err = batchCounter(revision); err != nil {
		return "", nil, err
	}
	checkpoint, err := base64.StdEncoding.Strict().DecodeString(encoded)
	return revision, checkpoint, err
}
func (s *BatchStore) audit(ctx context.Context, tx *sql.Tx, k SessionKey, kind string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_audit(id,page_id,workspace_id,kind,detail_json) VALUES(?,?,?,?,?)`, hex.EncodeToString(id), k.ArtifactID, k.WorkspaceID, kind, string(raw))
	return err
}
func (s *BatchStore) auditCapacity(ctx context.Context, tx *sql.Tx, page string, n int) error {
	if n == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_audit WHERE page_id=?`, page).Scan(&count); err != nil {
		return err
	}
	if n > s.config.Limits.AuditEvents-count {
		return ErrBatchCapacity
	}
	return nil
}

// Initialize stores a qualified checkpoint at revision 0. It creates no live
// process capability; restored state still requires a fresh owner and admission.
func (s *BatchStore) Initialize(ctx context.Context, lease OwnerLease, checkpoint []byte) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(checkpoint) > s.config.Limits.CheckpointBytes {
		return ErrInvalid
	}
	checkpoint = append([]byte(nil), checkpoint...)
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, lease.Key, "initialize", nil, "", ""); err != nil {
			return err
		}
		if err := s.config.Owners.Check(ctx, tx, lease); err != nil {
			return err
		}
		var inventory []BatchTargetIdentity
		if s.targetsEnabled() {
			var err error
			inventory, err = s.config.Targets.Adapter.ValidateTargets(ctx, append([]byte(nil), checkpoint...))
			if err != nil {
				return err
			}
			if !validTargetInventory(inventory, s.config.Targets.Limits.Targets) {
				return ErrInvalid
			}
		} else if err := s.config.Adapter.ValidateCheckpoint(ctx, append([]byte(nil), checkpoint...)); err != nil {
			return err
		}
		metadata, err := s.describeCheckpoint(ctx, checkpoint)
		if err != nil {
			return err
		}
		if err = s.checkpointCapacity(ctx, tx, lease.Key.ArtifactID, len(checkpoint), metadata); err != nil {
			return err
		}
		encoded := base64.StdEncoding.EncodeToString(checkpoint)
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,target_tracking) VALUES(?,?,?,'0',?,?)`, lease.Key.ArtifactID, lease.Key.WorkspaceID, lease.Key.ApplicationID, encoded, s.targetsEnabled()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation,checkpoint_metadata_json) VALUES(?,?,'0',?,?,?,?)`, lease.Key.ArtifactID, lease.Key.WorkspaceID, encoded, len(checkpoint), strconv.FormatInt(lease.Generation, 10), checkpointMetadataSQL(metadata))
		if err != nil {
			return err
		}
		if err = s.initializeTargets(ctx, tx, lease.Key, inventory); err != nil {
			return err
		}
		if err = s.audit(ctx, tx, lease.Key, "initialized", map[string]string{"revision": "0"}); err != nil {
			return err
		}
		if err = s.checkpointStoredCapacity(ctx, tx, lease.Key.ArtifactID); err != nil {
			return err
		}
		if err = s.config.Authority(ctx, tx, BatchAccess{store: s, final: true, Intent: "initialize", Scope: lease.Key}); err != nil {
			return err
		}
		return s.config.Owners.Check(ctx, tx, lease)
	})
}

// RegisterTask records immutable attribution and policy after fresh host
// admission. It never creates a task as a side effect of querying/submitting.
func (s *BatchStore) RegisterTask(ctx context.Context, lease OwnerLease, id, participant string, allowPartial bool) error {
	if !batchID(id) || !batchID(participant) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, lease.Key, "task", nil, id, participant); err != nil {
			return err
		}
		if _, _, err := batchScopeMatches(ctx, tx, lease.Key); err != nil {
			return err
		}
		var oldParticipant string
		var oldPartial int
		err := tx.QueryRowContext(ctx, `SELECT participant_ref,allow_partial FROM mooring_collab_tasks WHERE page_id=? AND task_id=?`, lease.Key.ArtifactID, id).Scan(&oldParticipant, &oldPartial)
		if err == nil {
			if oldParticipant != participant || (oldPartial != 0) != allowPartial {
				return ErrOperationConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_tasks WHERE page_id=?`, lease.Key.ArtifactID).Scan(&count); err != nil {
			return err
		}
		if count >= s.config.Limits.Tasks {
			return ErrBatchCapacity
		}
		if err = s.auditCapacity(ctx, tx, lease.Key.ArtifactID, 1); err != nil {
			return err
		}
		if err = s.config.Owners.Check(ctx, tx, lease); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_tasks(page_id,workspace_id,task_id,participant_ref,allow_partial,status) VALUES(?,?,?,?,?,'running')`, lease.Key.ArtifactID, lease.Key.WorkspaceID, id, participant, allowPartial); err != nil {
			return err
		}
		return s.audit(ctx, tx, lease.Key, "task_registered", map[string]any{"task_id": id, "participant_ref": participant, "allow_partial": allowPartial})
	})
}

// Status authorizes before lookup and binds the immutable expected fingerprint.
// It requires no current writer lease: a replacement host may recover outcomes.
func (s *BatchStore) Status(ctx context.Context, key SessionKey, id, expectedFingerprint string) (*BatchOperationResult, error) {
	if !batchID(id) || len(expectedFingerprint) != 71 || expectedFingerprint[:7] != "sha256:" {
		return nil, ErrInvalid
	}
	for _, c := range expectedFingerprint[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, ErrInvalid
		}
	}
	var result *BatchOperationResult
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, key, "outcome", nil, "", ""); err != nil {
			return err
		}
		if _, _, err := batchScopeMatches(ctx, tx, key); err != nil {
			return err
		}
		var fingerprint, raw string
		err := tx.QueryRowContext(ctx, `SELECT fingerprint,result_json FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, key.ArtifactID, id).Scan(&fingerprint, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if fingerprint != expectedFingerprint {
			return ErrOperationConflict
		}
		var stored BatchOperationResult
		if err = json.Unmarshal([]byte(raw), &stored); err != nil {
			return err
		}
		if stored.ID != id || !validStoredBatchOperation(stored) {
			return ErrInvalid
		}
		result = &stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *BatchStore) saveChange(ctx context.Context, tx *sql.Tx, k SessionKey, c BatchChange, result BatchChangeResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var task any
	if c.TaskID != "" {
		task = c.TaskID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,task_id,result_json) VALUES(?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, c.ID, c.MembershipHash, task, string(raw)); err != nil {
		return err
	}
	for i, op := range c.Operations {
		packet, e := json.Marshal(result.Operations[i])
		if e != nil {
			return e
		}
		dependencies, err := json.Marshal(op.Dependencies)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_operations(page_id,workspace_id,op_id,change_id,participant_ref,fingerprint,result_json,dependencies_json,target_effects_complete) VALUES(?,?,?,?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, op.ID, c.ID, op.Participant, op.Fingerprint, string(packet), string(dependencies), s.targetsEnabled()); err != nil {
			return err
		}
	}
	return s.audit(ctx, tx, k, "change_"+result.Status, result)
}
func (s *BatchStore) checkpointCapacity(ctx context.Context, tx *sql.Tx, page string, size int, metadata []byte) error {
	var count int
	var bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(decoded_bytes+coalesce(length(CAST(checkpoint_metadata_json AS BLOB)),0)),0) FROM mooring_collab_checkpoints WHERE page_id=?`, page).Scan(&count, &bytes); err != nil {
		return err
	}
	l := s.config.Limits
	if size > l.CheckpointBytes || count >= l.Checkpoints || int64(size)+int64(len(metadata)) > l.TotalCheckpointBytes-bytes {
		return ErrBatchCapacity
	}
	return nil
}

// Recheck complete persisted usage after audit/output/trigger effects, before
// final authority and owner fences. No checkpoint payload is scanned into Go.
func (s *BatchStore) checkpointStoredCapacity(ctx context.Context, tx *sql.Tx, page string) error {
	var count int
	var bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(decoded_bytes+coalesce(length(CAST(checkpoint_metadata_json AS BLOB)),0)),0) FROM mooring_collab_checkpoints WHERE page_id=?`, page).Scan(&count, &bytes); err != nil {
		return err
	}
	if count > s.config.Limits.Checkpoints || bytes < 0 || bytes > s.config.Limits.TotalCheckpointBytes {
		return ErrBatchCapacity
	}
	return nil
}
func (s *BatchStore) saveCheckpoint(ctx context.Context, tx *sql.Tx, lease OwnerLease, revision string, checkpoint, metadata []byte) error {
	k := lease.Key
	encoded := base64.StdEncoding.EncodeToString(checkpoint)
	if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation,checkpoint_metadata_json) VALUES(?,?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, revision, encoded, len(checkpoint), strconv.FormatInt(lease.Generation, 10), checkpointMetadataSQL(metadata)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE mooring_collab_documents SET revision=?,checkpoint_base64=? WHERE page_id=?`, revision, encoded, k.ArtifactID)
	return err
}
func savedBatchChange(c BatchChange, rev uint64) (BatchChangeResult, error) {
	first, last, err := batchRange(rev, len(c.Operations))
	if err != nil {
		return BatchChangeResult{}, err
	}
	result := BatchChangeResult{ID: c.ID, Status: "saved", FirstRevision: first, LastRevision: last, Operations: make([]BatchOperationResult, 0, len(c.Operations))}
	for i, op := range c.Operations {
		result.Operations = append(result.Operations, BatchOperationResult{ID: op.ID, Status: "saved", Revision: strconv.FormatUint(rev+uint64(i)+1, 10), Issuer: "crewship", Durability: "node"})
	}
	return result, nil
}

// ReadCheckpoint is a host recovery read, not a participant delivery path.
func (s *BatchStore) ReadCheckpoint(ctx context.Context, key SessionKey) (string, []byte, error) {
	var revision string
	var checkpoint []byte
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, key, "read_checkpoint", nil, "", ""); err != nil {
			return err
		}
		var err error
		revision, checkpoint, err = batchScopeMatches(ctx, tx, key)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return revision, checkpoint, nil
}
func (s *BatchStore) CancelTask(ctx context.Context, lease OwnerLease, id string) error {
	if !batchID(id) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, lease.Key, "task", nil, id, ""); err != nil {
			return err
		}
		if _, _, err := batchScopeMatches(ctx, tx, lease.Key); err != nil {
			return err
		}
		var actor, status string
		if err := tx.QueryRowContext(ctx, `SELECT participant_ref,status FROM mooring_collab_tasks WHERE page_id=? AND task_id=?`, lease.Key.ArtifactID, id).Scan(&actor, &status); err != nil {
			return err
		}
		if status == "cancelled" {
			return nil
		}
		if err := s.auditCapacity(ctx, tx, lease.Key.ArtifactID, 1); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, lease.Key, "task", nil, id, actor); err != nil {
			return err
		}
		if err := s.config.Owners.Check(ctx, tx, lease); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mooring_collab_tasks SET status='cancelled' WHERE page_id=? AND task_id=?`, lease.Key.ArtifactID, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, lease.Key, "task_cancelled", map[string]string{"task_id": id})
	})
}
