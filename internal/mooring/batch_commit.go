package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

func (s *BatchStore) loadKnown(ctx context.Context, tx *sql.Tx, k SessionKey, changes []BatchChange) (map[int]BatchChangeResult, error) {
	known := map[int]BatchChangeResult{}
	for index, c := range changes {
		var membership, raw string
		err := tx.QueryRowContext(ctx, `SELECT membership_hash,result_json FROM mooring_collab_changes WHERE page_id=? AND change_id=?`, k.ArtifactID, c.ID).Scan(&membership, &raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		exists := err == nil
		if exists {
			if membership != c.MembershipHash {
				return nil, ErrOperationConflict
			}
			var result BatchChangeResult
			if err = json.Unmarshal([]byte(raw), &result); err != nil {
				return nil, err
			}
			if !validStoredBatchChange(c, result) {
				return nil, ErrInvalid
			}
			known[index] = result
		}
		for _, op := range c.Operations {
			var fingerprint, change string
			err = tx.QueryRowContext(ctx, `SELECT fingerprint,change_id FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, k.ArtifactID, op.ID).Scan(&fingerprint, &change)
			if errors.Is(err, sql.ErrNoRows) {
				if exists {
					return nil, ErrInvalid
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if !exists || fingerprint != op.Fingerprint || change != c.ID {
				return nil, ErrOperationConflict
			}
		}
	}
	return known, nil
}
func (s *BatchStore) ledgerCapacity(ctx context.Context, tx *sql.Tx, page string, changes []BatchChange, known map[int]BatchChangeResult) error {
	fresh, operations := 0, 0
	for i, c := range changes {
		if _, ok := known[i]; !ok {
			fresh++
			operations += len(c.Operations)
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, page).Scan(&count); err != nil {
		return err
	}
	if operations > 0 && operations > s.config.Limits.LedgerOperations-count {
		return ErrBatchCapacity
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_changes WHERE page_id=?`, page).Scan(&count); err != nil {
		return err
	}
	if fresh > 0 && fresh > s.config.Limits.Changes-count {
		return ErrBatchCapacity
	}
	return s.auditCapacity(ctx, tx, page, fresh)
}
func (s *BatchStore) validateTasks(ctx context.Context, tx *sql.Tx, k SessionKey, changes []BatchChange, known map[int]BatchChangeResult) (map[int]*BatchRejection, error) {
	reasons := map[int]*BatchRejection{}
	counts := map[string]int{}
	for i, c := range changes {
		if _, ok := known[i]; !ok && c.TaskID != "" {
			counts[c.TaskID]++
		}
	}
	for i, c := range changes {
		if _, ok := known[i]; ok || c.TaskID == "" {
			continue
		}
		var actor, status string
		var partial int
		err := tx.QueryRowContext(ctx, `SELECT participant_ref,allow_partial,status FROM mooring_collab_tasks WHERE page_id=? AND task_id=?`, k.ArtifactID, c.TaskID).Scan(&actor, &partial, &status)
		if err != nil {
			return nil, err
		} // Unknown task never creates a different task identity.
		for _, op := range c.Operations {
			if op.Participant != actor {
				return nil, ErrInvalid
			}
		}
		if partial == 0 {
			var prior int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_changes WHERE page_id=? AND task_id=?`, k.ArtifactID, c.TaskID).Scan(&prior); err != nil {
				return nil, err
			}
			if counts[c.TaskID] != 1 || prior != 0 {
				return nil, ErrInvalid
			}
		}
		if status == "cancelled" {
			reasons[i] = batchReason("task_cancelled")
		}
	}
	return reasons, nil
}
func dependencyOutcome(ctx context.Context, tx *sql.Tx, page, id string) (*BatchOperationResult, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT result_json FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, page, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result BatchOperationResult
	err = json.Unmarshal([]byte(raw), &result)
	if err == nil && (result.ID != id || !validStoredBatchOperation(result)) {
		return nil, ErrInvalid
	}
	return &result, err
}
func (s *BatchStore) deniedReason(err error) (*BatchRejection, error) {
	if err == nil {
		return nil, nil
	}
	var denied *BatchDenied
	if errors.As(err, &denied) && (denied.Code == "permission_revoked" || denied.Code == "forbidden" || denied.Code == "task_cancelled") {
		return batchReason(denied.Code), nil
	}
	return nil, err
}

// Commit atomically persists fresh outcomes and accepted checkpoints. Known
// results are returned unchanged, including after a lost response/reconnection.
// Any SQL/engine/owner error returns no terminal result; callers query Status.
func (s *BatchStore) Commit(ctx context.Context, request BatchRequest) (BatchResult, error) {
	return s.commit(ctx, request, nil)
}

func (s *BatchStore) commit(ctx context.Context, request BatchRequest, session *sessionCommitMode) (BatchResult, error) {
	if err := s.ready(); err != nil {
		return BatchResult{}, err
	}
	changes, err := normalizeBatch(request)
	if err != nil {
		return BatchResult{}, err
	}
	if session != nil {
		session.members = make(map[string]sessionMembership)
		for _, change := range changes {
			for _, op := range change.Operations {
				session.members[op.ID] = sessionMembership{change.ID, change.MembershipHash}
			}
		}
	}
	result := BatchResult{}
	emitted := false
	eventChanges := []BatchChangeResult{}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		key := request.Lease.Key
		if session != nil {
			if err := s.config.Owners.Check(ctx, tx, request.Lease); err != nil {
				return err
			}
		}
		// No operation/task existence lookup precedes current scoped authorization.
		for _, c := range changes {
			for i := range c.Operations {
				op := &c.Operations[i]
				if e := s.authorize(ctx, tx, key, "outcome", op, c.TaskID, op.Participant); e != nil {
					return e
				}
			}
		}
		var revision string
		var checkpoint []byte
		var e error
		if session == nil {
			revision, checkpoint, e = batchScopeMatches(ctx, tx, key)
		} else {
			revision, checkpoint, e = sessionScopeCheckpoint(ctx, tx, key, session.basis)
			request.ExpectedRevision = revision
		}
		if e != nil {
			return e
		}
		if e = s.requireTargetMode(ctx, tx, key.ArtifactID); e != nil {
			return e
		}
		rev, e := batchCounter(revision)
		if e != nil {
			return e
		}
		known, e := s.loadKnown(ctx, tx, key, changes)
		if e != nil {
			return e
		}
		if e = s.ledgerCapacity(ctx, tx, key.ArtifactID, changes, known); e != nil {
			return e
		}
		if e = s.targetCapacity(ctx, tx, key.ArtifactID, changes, known); e != nil {
			return e
		}
		taskReasons, e := s.validateTasks(ctx, tx, key, changes, known)
		if e != nil {
			return e
		}
		failures, e := s.preflightBatch(ctx, tx, key, changes, known, taskReasons, request.ExpectedRevision, revision)
		if e != nil {
			return e
		}
		plan := planBatch(changes, known, failures)
		done := map[int]bool{}
		outcomes := map[string]BatchOperationResult{}
		for index, r := range known {
			done[index] = true
			for _, op := range r.Operations {
				outcomes[op.ID] = op
			}
		}
		accepted := map[int]BatchChangeResult{}
		for index, r := range known {
			accepted[index] = r
		}
		for len(done) < len(changes) {
			next := -1
			for i := range changes {
				if done[i] {
					continue
				}
				ready := true
				for _, d := range plan.Edges[i] {
					if !done[d] {
						ready = false
						break
					}
				}
				if ready || plan.Initial[i] != nil {
					next = i
					break
				}
			}
			if next < 0 {
				return ErrInvalid
			}
			c := changes[next]
			var reason *BatchRejection
			rootID := c.Operations[0].ID
			if failure := plan.Initial[next]; failure != nil {
				reason = failure.Reason
				rootID = failure.OperationID
			}
			if reason == nil {
				for _, op := range c.Operations {
					for _, dep := range op.Dependencies {
						if index, ok := plan.ByOperation[dep]; ok && index == next {
							continue
						}
						prior, ok := outcomes[dep]
						if !ok {
							stored, e := dependencyOutcome(ctx, tx, key.ArtifactID, dep)
							if e != nil {
								return e
							}
							if stored == nil {
								reason = batchReason("unknown_dependency")
								rootID = op.ID
								break
							}
							prior = *stored
						}
						if prior.Status == "rejected" {
							if prior.Reason == nil || !validBatchRejection(prior.Reason) {
								return ErrInvalid
							}
							reason = dependencyReason(prior)
							break
						}
					}
					if reason != nil {
						break
					}
				}
			}
			if reason == nil && request.ExpectedRevision != revision {
				reason = batchReason("precondition_failed")
			}
			if reason == nil {
				if _, _, e := batchRange(rev, len(c.Operations)); e != nil {
					reason = batchReason("capacity_exhausted")
				}
			}
			for i := range c.Operations {
				op := &c.Operations[i]
				denied, e := s.deniedReason(s.authorize(ctx, tx, key, "write", op, c.TaskID, op.Participant))
				if e != nil {
					return e
				}
				if reason == nil && denied != nil {
					reason = denied
					rootID = op.ID
				}
			}
			var candidate []byte
			var targetRows []preparedTargetRow
			var targetCandidate BatchTargetCandidate
			var targetContext BatchPrepareContext
			var causalContext BatchCausalPrepareContext
			if reason == nil {
				isolated := cloneBatchChange(c)
				var failure *PreparedFailure
				if s.targetsEnabled() {
					if s.config.Targets.ReadPolicy == CausalTargetReadPolicy {
						inventory, inventoryErr := s.config.Targets.Adapter.ValidateTargets(ctx, append([]byte(nil), checkpoint...))
						if inventoryErr != nil {
							return inventoryErr
						}
						causalContext, e = s.qualifyCausalReads(ctx, tx, key, c, inventory, s.config.Targets.HistoryBudget)
						if e != nil {
							return e
						}
						failure, e = causalTargetReadFailure(c, causalContext, inventory)
						if e != nil {
							return e
						}
						rawContext, marshalErr := json.Marshal(causalContext)
						if marshalErr != nil {
							return marshalErr
						}
						var isolatedContext BatchCausalPrepareContext
						if e = json.Unmarshal(rawContext, &isolatedContext); e != nil {
							return e
						}
						if failure == nil {
							targetCandidate, failure, e = s.config.Targets.Adapter.(CausalTargetBatchAdapter).PrepareCausalTargets(ctx, append([]byte(nil), checkpoint...), isolated, isolatedContext)
						}
					} else {
						targetContext, e = s.targetContext(ctx, tx, key, c, rev)
						if e != nil {
							return e
						}
						if e = s.confirmTargetContext(ctx, checkpoint, c, targetContext); e != nil {
							return e
						}
						failure = confirmedTargetReadFailure(c, targetContext)
						rawContext, _ := json.Marshal(targetContext)
						var isolatedContext BatchPrepareContext
						if e = json.Unmarshal(rawContext, &isolatedContext); e != nil {
							return e
						}
						if failure == nil {
							targetCandidate, failure, e = s.config.Targets.Adapter.PrepareTargets(ctx, append([]byte(nil), checkpoint...), isolated, isolatedContext)
						}
					}
					candidate = append([]byte(nil), targetCandidate.Checkpoint...)
				} else {
					candidate, failure, e = s.config.Adapter.Apply(ctx, append([]byte(nil), checkpoint...), isolated)
				}
				if failure != nil {
					if !validPreparedFailure(c, failure) {
						return ErrInvalid
					}
					reason = failure.Reason
					rootID = failure.OperationID
				}
				if e != nil {
					return e
				}
				if reason != nil && !validBatchRejection(reason) {
					return ErrInvalid
				}
				if reason == nil {
					if len(candidate) > s.config.Limits.CheckpointBytes {
						reason = batchReason("quota_exceeded")
					} else if s.targetsEnabled() {
						validated, e := s.config.Targets.Adapter.ValidateTargets(ctx, append([]byte(nil), candidate...))
						if e != nil {
							return e
						}
						if s.config.Targets.ReadPolicy == CausalTargetReadPolicy {
							targetRows, e = s.validateCausalTargetCandidate(c, causalContext, targetCandidate, validated)
						} else {
							targetRows, e = s.validateTargetCandidate(c, targetContext, targetCandidate, validated)
						}
						if e != nil {
							return e
						}
					} else if e = s.config.Adapter.ValidateCheckpoint(ctx, append([]byte(nil), candidate...)); e != nil {
						return e
					}
					e = s.checkpointCapacity(ctx, tx, key.ArtifactID, len(candidate))
					if errors.Is(e, ErrBatchCapacity) {
						reason = batchReason("quota_exceeded")
					} else if e != nil {
						return e
					}
				}
			}
			// Recheck every original participant after expensive engine validation.
			// A prepared apply does not carry authorization and cannot issue Saved.
			for i := range c.Operations {
				op := &c.Operations[i]
				denied, e := s.deniedReason(s.authorize(ctx, tx, key, "write", op, c.TaskID, op.Participant))
				if e != nil {
					return e
				}
				if denied != nil && reason == nil {
					reason = denied
				}
			}
			if e = s.config.Owners.Check(ctx, tx, request.Lease); e != nil {
				return e
			}
			var stored BatchChangeResult
			if reason != nil {
				stored = rejectedBatchChange(c, reason, rootID)
			} else {
				stored, e = savedBatchChange(c, rev)
				if e != nil {
					return e
				}
				if s.targetsEnabled() {
					if e = s.targetPreparedByteCapacity(ctx, tx, key, c, targetRows); e != nil {
						return e
					}
				}
				if e = s.saveCheckpoint(ctx, tx, request.Lease, stored.LastRevision, candidate); e != nil {
					return e
				}
				checkpoint = append([]byte(nil), candidate...)
				rev, e = batchCounter(stored.LastRevision)
				if e != nil {
					return e
				}
			}
			if e = s.saveChange(ctx, tx, key, c, stored); e != nil {
				return e
			}
			if stored.Status == "saved" && s.targetsEnabled() {
				if e = persistTargetRows(ctx, tx, key, targetRows); e != nil {
					return e
				}
			}
			eventChanges = append(eventChanges, stored)
			accepted[next] = stored
			done[next] = true
			for _, op := range stored.Operations {
				outcomes[op.ID] = op
			}
			emitted = true
		}
		result.Revision = strconv.FormatUint(rev, 10)
		result.Changes = make([]BatchChangeResult, len(changes))
		for i := range changes {
			result.Changes[i] = accepted[i]
		}
		if session != nil {
			if e = session.prepareOutput(ctx, tx, request.Lease, result, checkpoint); e != nil {
				return e
			}
		}
		if emitted {
			// Rejected operations also retain immutable dependency history. A prior
			// candidate may consume the remaining budget after pre-admission checks;
			// charge the complete transaction before final authority/fence checks.
			// All-known retries skip this check and retain their original outcome.
			if e = s.targetByteCapacity(ctx, tx, key.ArtifactID, 0); e != nil {
				return e
			}
			// A later change's expensive apply can expire an earlier participant's
			// clock-bound run/grant even while SQLite blocks permission-table writes.
			// Revalidate every fresh accepted author after ALL application work. Any
			// changed/uncertain authority aborts the entire transaction; no prepared
			// Saved or terminal rejection can escape, and independent work remains a
			// client draft to retry/query rather than an incorrectly attributed commit.
			for index, c := range changes {
				if _, existing := known[index]; existing || accepted[index].Status != "saved" {
					continue
				}
				for i := range c.Operations {
					op := &c.Operations[i]
					if e = s.authorize(ctx, tx, key, "write", op, c.TaskID, op.Participant); e != nil {
						return e
					}
				}
			}
			if e = s.config.Owners.Check(ctx, tx, request.Lease); e != nil {
				return e
			}
		}
		if session != nil && !emitted {
			if e = s.config.Owners.Check(ctx, tx, request.Lease); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return BatchResult{}, err
	}
	if emitted && s.config.AfterCommit != nil {
		raw, _ := json.Marshal(BatchResult{Revision: result.Revision, Changes: eventChanges})
		var eventResult BatchResult
		_ = json.Unmarshal(raw, &eventResult)
		s.config.AfterCommit(BatchCommitEvent{Scope: request.Lease.Key, Result: eventResult})
	}
	return result, nil
}
