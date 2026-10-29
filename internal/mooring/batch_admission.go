package mooring

import (
	"context"
	"database/sql"
)

func batchOperationChanges(changes []BatchChange) map[string]int {
	out := map[string]int{}
	for i, c := range changes {
		for _, op := range c.Operations {
			out[op.ID] = i
		}
	}
	return out
}

// Definite failures make a whole atomic change nonexecuting. Propagate them
// before SCC detection so a dead change's outgoing edges cannot invent a cycle.
func propagateBatchFailures(changes []BatchChange, known map[int]BatchChangeResult, failures map[int]*PreparedFailure) {
	byID := batchOperationChanges(changes)
	for progress := true; progress; {
		progress = false
		for index, c := range changes {
			if _, ok := known[index]; ok || failures[index] != nil {
				continue
			}
			for _, op := range c.Operations {
				for _, dep := range op.Dependencies {
					other, ok := byID[dep]
					if !ok || other == index {
						continue
					}
					var result BatchChangeResult
					if saved, ok := known[other]; ok {
						result = saved
					} else if failure := failures[other]; failure != nil {
						result = rejectedBatchChange(changes[other], failure.Reason, failure.OperationID)
					} else {
						continue
					}
					for _, prior := range result.Operations {
						if prior.ID == dep && prior.Status == "rejected" {
							failures[index] = &PreparedFailure{OperationID: op.ID, Reason: dependencyReason(prior)}
							progress = true
							break
						}
					}
					if failures[index] != nil {
						break
					}
				}
				if failures[index] != nil {
					break
				}
			}
		}
	}
}

func (s *BatchStore) preflightBatch(ctx context.Context, tx *sql.Tx, key SessionKey, changes []BatchChange, known map[int]BatchChangeResult, taskReasons map[int]*BatchRejection, expected, revision string) (map[int]*PreparedFailure, error) {
	failures := map[int]*PreparedFailure{}
	byID := batchOperationChanges(changes)
	external := map[string]*BatchOperationResult{}
	for index, c := range changes {
		if _, ok := known[index]; ok {
			continue
		}
		if reason := taskReasons[index]; reason != nil {
			failures[index] = &PreparedFailure{c.Operations[0].ID, reason}
		} else if expected != revision {
			failures[index] = &PreparedFailure{c.Operations[0].ID, batchReason("precondition_failed")}
		}
		for i := range c.Operations {
			op := &c.Operations[i]
			reason, err := s.deniedReason(s.authorize(ctx, tx, key, "write", op, c.TaskID, op.Participant))
			if err != nil {
				return nil, err
			}
			if failures[index] == nil && reason != nil {
				failures[index] = &PreparedFailure{op.ID, reason}
			}
		}
		if failures[index] != nil {
			continue
		}
		for _, op := range c.Operations {
			for _, dep := range op.Dependencies {
				if _, ok := byID[dep]; ok {
					continue
				}
				prior, cached := external[dep]
				if !cached {
					var err error
					prior, err = dependencyOutcome(ctx, tx, key.ArtifactID, dep)
					if err != nil {
						return nil, err
					}
					external[dep] = prior
				}
				if prior == nil {
					failures[index] = &PreparedFailure{op.ID, batchReason("unknown_dependency")}
					break
				}
				if prior.Status == "rejected" {
					if !validBatchRejection(prior.Reason) {
						return nil, ErrInvalid
					}
					failures[index] = &PreparedFailure{op.ID, dependencyReason(*prior)}
					break
				}
			}
			if failures[index] != nil {
				break
			}
		}
	}
	propagateBatchFailures(changes, known, failures)
	return failures, nil
}
