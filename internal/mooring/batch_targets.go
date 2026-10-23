package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strconv"
)

func (s *BatchStore) targetsEnabled() bool {
	return s.config.Targets != nil && s.config.Targets.Enabled
}
func (s *BatchStore) targetConfigValid() bool {
	if !s.targetsEnabled() {
		return true
	}
	c := s.config.Targets
	return c.Adapter != nil && c.Limits.Targets > 0 && c.Limits.Targets <= 65536 && c.Limits.Effects > 0 && c.Limits.EffectsPerOperation > 0 && c.Limits.EffectsPerOperation <= 4096 && c.Limits.ProvenanceBytes > 0
}
func validTargetInventory(in []BatchTargetIdentity, limit int) bool {
	if in == nil || len(in) > limit {
		return false
	}
	seen := map[string]bool{}
	for _, t := range in {
		if !batchID(t.TargetID) || seen[t.TargetID] {
			return false
		}
		seen[t.TargetID] = true
	}
	return true
}
func (s *BatchStore) initializeTargets(ctx context.Context, tx *sql.Tx, k SessionKey, in []BatchTargetIdentity) error {
	if !s.targetsEnabled() {
		return nil
	}
	if !validTargetInventory(in, s.config.Targets.Limits.Targets) {
		return ErrInvalid
	}
	for _, t := range in {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,?,'0','0',?)`, k.ArtifactID, k.WorkspaceID, t.TargetID, t.Deleted); err != nil {
			return err
		}
	}
	return nil
}
func (s *BatchStore) requireTargetMode(ctx context.Context, tx *sql.Tx, page string) error {
	var tracking int
	if err := tx.QueryRowContext(ctx, `SELECT target_tracking FROM mooring_collab_documents WHERE page_id=?`, page).Scan(&tracking); err != nil {
		return err
	}
	if (tracking == 1) != s.targetsEnabled() {
		return ErrInvalid
	}
	return nil
}
func (s *BatchStore) targetContext(ctx context.Context, tx *sql.Tx, k SessionKey, c BatchChange, head uint64) (BatchPrepareContext, error) {
	result := BatchPrepareContext{ReadPolicy: "agent_confirmed_current", DocumentRevision: strconv.FormatUint(head, 10), Targets: []BatchTargetVersion{}, OperationRevisions: []BatchOperationRevision{}}
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id,target_id,revision,structure_revision,deleted FROM mooring_collab_target_versions WHERE page_id=? ORDER BY target_id`, k.ArtifactID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var t BatchTargetVersion
		var workspace string
		if err = rows.Scan(&workspace, &t.TargetID, &t.Revision, &t.StructureRevision, &t.Deleted); err != nil {
			return result, err
		}
		if workspace != k.WorkspaceID || !batchID(t.TargetID) {
			return result, ErrInvalid
		}
		if _, err = batchCounter(t.Revision); err != nil {
			return result, err
		}
		if _, err = batchCounter(t.StructureRevision); err != nil {
			return result, err
		}
		targetRevision, _ := batchCounter(t.Revision)
		structureRevision, _ := batchCounter(t.StructureRevision)
		if structureRevision > targetRevision || targetRevision > head {
			return result, ErrInvalid
		}
		result.Targets = append(result.Targets, t)
		if len(result.Targets) > s.config.Targets.Limits.Targets {
			return result, ErrBatchCapacity
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if _, _, err = batchRange(head, len(c.Operations)); err != nil {
		return result, err
	}
	for i, op := range c.Operations {
		result.OperationRevisions = append(result.OperationRevisions, BatchOperationRevision{op.ID, strconv.FormatUint(head+uint64(i)+1, 10)})
	}
	return result, nil
}
func (s *BatchStore) targetCapacity(ctx context.Context, tx *sql.Tx, page string, changes []BatchChange, known map[int]BatchChangeResult) error {
	if !s.targetsEnabled() {
		return nil
	}
	operations := 0
	var dependencies int64
	for i, c := range changes {
		if _, exists := known[i]; exists {
			continue
		}
		operations += len(c.Operations)
		for _, op := range c.Operations {
			raw, err := json.Marshal(op.Dependencies)
			if err != nil {
				return err
			}
			if int64(len(raw)) > math.MaxInt64-dependencies {
				return ErrBatchCapacity
			}
			dependencies += int64(len(raw))
		}
	}
	if operations == 0 {
		return nil
	}
	if s.targetsEnabled() {
		if err := s.targetByteCapacity(ctx, tx, page, dependencies); err != nil {
			return err
		}
	}
	if !s.targetsEnabled() || operations == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_target_effects WHERE page_id=?`, page).Scan(&count); err != nil {
		return err
	}
	// Conservative pre-admission reservation bounds all prepared output effects.
	if operations > (s.config.Targets.Limits.Effects-count)/s.config.Targets.Limits.EffectsPerOperation {
		return ErrBatchCapacity
	}
	return nil
}

type preparedTargetRow struct {
	Before      *BatchTargetVersion
	After       BatchTargetVersion
	Effect      BatchTargetEffect
	OperationID string
	Revision    string
}

// validateTargetCandidate checks host bindings and current read observations.
// It does not implement stale causal chains: those remain an explicit followup.
func (s *BatchStore) validateTargetCandidate(c BatchChange, trusted BatchPrepareContext, candidate BatchTargetCandidate, validated []BatchTargetIdentity) ([]preparedTargetRow, error) {
	limit := s.config.Targets.Limits
	if !validTargetInventory(candidate.Inventory, limit.Targets) || !validTargetInventory(validated, limit.Targets) || len(candidate.Effects) != len(c.Operations) {
		return nil, ErrInvalid
	}
	expected := map[string]bool{}
	for _, v := range validated {
		expected[v.TargetID] = v.Deleted
	}
	if len(expected) != len(candidate.Inventory) {
		return nil, ErrInvalid
	}
	for _, v := range candidate.Inventory {
		deleted, ok := expected[v.TargetID]
		if !ok || deleted != v.Deleted {
			return nil, ErrInvalid
		}
	}
	state := map[string]BatchTargetVersion{}
	for _, t := range trusted.Targets {
		state[t.TargetID] = t
	}
	// Every supplied observation must first be confirmed at the authoritative
	// entry snapshot. A guessed prospective revision is not a confirmed read.
	for _, op := range c.Operations {
		for _, r := range op.Reads {
			t, ok := state[r.TargetID]
			if !ok || t.Deleted || t.Revision != r.Revision || (r.StructureRevision != nil && t.StructureRevision != *r.StructureRevision) {
				return nil, ErrInvalid
			}
		}
	}
	var out []preparedTargetRow
	for i, op := range c.Operations {
		effects := candidate.Effects[i]
		if effects.OperationID != op.ID || effects.Targets == nil || len(effects.Targets) > limit.EffectsPerOperation {
			return nil, ErrInvalid
		}
		for _, r := range op.Reads {
			t, ok := state[r.TargetID]
			if !ok || t.Deleted || t.Revision != r.Revision || (r.StructureRevision != nil && t.StructureRevision != *r.StructureRevision) {
				return nil, ErrInvalid
			}
		}
		seen := map[string]bool{}
		for _, effect := range effects.Targets {
			if !batchID(effect.TargetID) || seen[effect.TargetID] || (!effect.ContentChanged && !effect.StructureChanged) || (effect.Created && effect.Deleted) || ((effect.Created || effect.Deleted) && !effect.StructureChanged) {
				return nil, ErrInvalid
			}
			seen[effect.TargetID] = true
			old, exists := state[effect.TargetID]
			if effect.Created == exists || (exists && old.Deleted) {
				return nil, ErrInvalid
			}
			row := preparedTargetRow{Effect: effect, OperationID: op.ID, Revision: trusted.OperationRevisions[i].Revision}
			next := old
			if exists {
				copy := old
				row.Before = &copy
			} else {
				next = BatchTargetVersion{TargetID: effect.TargetID, StructureRevision: "0"}
			}
			next.Revision = row.Revision
			if effect.StructureChanged {
				next.StructureRevision = row.Revision
			}
			next.Deleted = effect.Deleted
			row.After = next
			state[effect.TargetID] = next
			out = append(out, row)
		}
	}
	if len(state) != len(expected) {
		return nil, ErrInvalid
	}
	for id, t := range state {
		deleted, ok := expected[id]
		if !ok || deleted != t.Deleted {
			return nil, ErrInvalid
		}
	}
	return out, nil
}
func persistTargetRows(ctx context.Context, tx *sql.Tx, k SessionKey, rows []preparedTargetRow) error {
	// Final per-target state first; effect rows refer to durable operation IDs.
	final := map[string]BatchTargetVersion{}
	for _, r := range rows {
		final[r.After.TargetID] = r.After
	}
	ids := make([]string, 0, len(final))
	for id := range final {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := final[id]
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,?,?,?,?) ON CONFLICT(page_id,target_id) DO UPDATE SET revision=excluded.revision,structure_revision=excluded.structure_revision,deleted=excluded.deleted`, k.ArtifactID, k.WorkspaceID, t.TargetID, t.Revision, t.StructureRevision, t.Deleted); err != nil {
			return err
		}
	}
	for _, r := range rows {
		raw, err := targetEffectRecord(k, r)
		if err != nil {
			return err
		}
		var before, structure any
		if r.Before != nil {
			before = r.Before.Revision
			structure = r.Before.StructureRevision
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_target_effects(page_id,workspace_id,op_id,target_id,revision,before_revision,before_structure_revision,content_changed,structure_changed,created,deleted,record_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, r.OperationID, r.Effect.TargetID, r.Revision, before, structure, r.Effect.ContentChanged, r.Effect.StructureChanged, r.Effect.Created, r.Effect.Deleted, string(raw)); err != nil {
			return err
		}
	}
	return nil
}

func (s *BatchStore) confirmTargetContext(ctx context.Context, checkpoint []byte, c BatchChange, trusted BatchPrepareContext) error {
	inventory, err := s.config.Targets.Adapter.ValidateTargets(ctx, append([]byte(nil), checkpoint...))
	if err != nil {
		return err
	}
	return validateConfirmedTargetContext(c, trusted, inventory)
}
func validateConfirmedTargetContext(c BatchChange, trusted BatchPrepareContext, inventory []BatchTargetIdentity) error {
	if trusted.ReadPolicy != "agent_confirmed_current" || !validTargetInventory(inventory, 65536) || len(inventory) != len(trusted.Targets) || len(c.Operations) == 0 || len(c.Operations) > 256 || len(trusted.OperationRevisions) != len(c.Operations) {
		return ErrInvalid
	}
	head, err := batchCounter(trusted.DocumentRevision)
	if err != nil {
		return err
	}
	if _, _, err = batchRange(head, len(c.Operations)); err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, t := range inventory {
		expected[t.TargetID] = t.Deleted
	}
	seen := map[string]bool{}
	for _, t := range trusted.Targets {
		deleted, ok := expected[t.TargetID]
		if !ok || seen[t.TargetID] || deleted != t.Deleted {
			return ErrInvalid
		}
		seen[t.TargetID] = true
		revision, err := batchCounter(t.Revision)
		if err != nil {
			return err
		}
		structure, err := batchCounter(t.StructureRevision)
		if err != nil || structure > revision || revision > head {
			return ErrInvalid
		}
	}
	seen = map[string]bool{}
	for i, op := range c.Operations {
		label := trusted.OperationRevisions[i]
		if !batchID(op.ID) || seen[op.ID] || label.OperationID != op.ID || label.Revision != strconv.FormatUint(head+uint64(i)+1, 10) {
			return ErrInvalid
		}
		seen[op.ID] = true
	}
	return nil
}
func confirmedTargetReadFailure(c BatchChange, trusted BatchPrepareContext) *PreparedFailure {
	state := map[string]BatchTargetVersion{}
	for _, t := range trusted.Targets {
		state[t.TargetID] = t
	}
	head, _ := batchCounter(trusted.DocumentRevision)
	for _, op := range c.Operations {
		base, err := batchCounter(op.BaseRevision)
		if err != nil {
			return &PreparedFailure{OperationID: op.ID, Reason: batchReason("invalid_request")}
		}
		if base > head {
			return &PreparedFailure{OperationID: op.ID, Reason: batchReason("precondition_failed")}
		}
		seen := map[string]bool{}
		for _, read := range op.Reads {
			if seen[read.TargetID] {
				return &PreparedFailure{OperationID: op.ID, Reason: batchReason("invalid_request")}
			}
			seen[read.TargetID] = true
			t, ok := state[read.TargetID]
			code := ""
			if !ok {
				code = "invalid_request"
			} else if t.Deleted {
				code = "target_deleted"
			} else if read.Revision != t.Revision || (read.StructureRevision != nil && *read.StructureRevision != t.StructureRevision) {
				code = "precondition_failed"
			}
			if code != "" {
				return &PreparedFailure{OperationID: op.ID, Reason: batchReason(code)}
			}
		}
	}
	return nil
}

// Logical quota counts exact UTF-8 encoding/json dependency arrays and effect
// records. It does not claim SQLite file-size accounting or JCS fingerprints.
func targetEffectRecord(k SessionKey, r preparedTargetRow) ([]byte, error) {
	type record struct {
		WorkspaceID             string  `json:"workspace_id"`
		PageID                  string  `json:"page_id"`
		OperationID             string  `json:"op_id"`
		TargetID                string  `json:"target_id"`
		Revision                string  `json:"revision"`
		BeforeRevision          *string `json:"before_revision"`
		BeforeStructureRevision *string `json:"before_structure_revision"`
		ContentChanged          bool    `json:"content_changed"`
		StructureChanged        bool    `json:"structure_changed"`
		Created                 bool    `json:"created"`
		Deleted                 bool    `json:"deleted"`
	}
	value := record{WorkspaceID: k.WorkspaceID, PageID: k.ArtifactID, OperationID: r.OperationID, TargetID: r.Effect.TargetID, Revision: r.Revision, ContentChanged: r.Effect.ContentChanged, StructureChanged: r.Effect.StructureChanged, Created: r.Effect.Created, Deleted: r.Effect.Deleted}
	if r.Before != nil {
		value.BeforeRevision = &r.Before.Revision
		value.BeforeStructureRevision = &r.Before.StructureRevision
	}
	return json.Marshal(value)
}
func (s *BatchStore) targetByteCapacity(ctx context.Context, tx *sql.Tx, page string, additional int64) error {
	if !s.targetsEnabled() {
		return nil
	}
	if additional < 0 {
		return ErrInvalid
	}
	var dependencies, effects int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(dependencies_json AS BLOB))),0) FROM mooring_collab_operations WHERE page_id=?`, page).Scan(&dependencies); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(record_json AS BLOB))),0) FROM mooring_collab_target_effects WHERE page_id=?`, page).Scan(&effects); err != nil {
		return err
	}
	if dependencies < 0 || effects < 0 || effects > math.MaxInt64-dependencies {
		return ErrInvalid
	}
	used := dependencies + effects
	limit := s.config.Targets.Limits.ProvenanceBytes
	if used > limit || additional > limit-used {
		return ErrBatchCapacity
	}
	return nil
}
func (s *BatchStore) targetPreparedByteCapacity(ctx context.Context, tx *sql.Tx, k SessionKey, c BatchChange, rows []preparedTargetRow) error {
	var additional int64
	for _, op := range c.Operations {
		raw, err := json.Marshal(op.Dependencies)
		if err != nil {
			return err
		}
		if int64(len(raw)) > math.MaxInt64-additional {
			return ErrBatchCapacity
		}
		additional += int64(len(raw))
	}
	for _, r := range rows {
		raw, err := targetEffectRecord(k, r)
		if err != nil {
			return err
		}
		if int64(len(raw)) > math.MaxInt64-additional {
			return ErrBatchCapacity
		}
		additional += int64(len(raw))
	}
	return s.targetByteCapacity(ctx, tx, k.ArtifactID, additional)
}
