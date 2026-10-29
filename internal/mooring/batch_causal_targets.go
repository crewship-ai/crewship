package mooring

import (
	"reflect"
	"strconv"
)

// causalTargetReadFailure validates every original binding before classifying
// any read. The SQL history qualifier, under this same writer, alone establishes
// Covered; this function must never manufacture that bit from a latest revision.
func causalTargetReadFailure(c BatchChange, trusted BatchCausalPrepareContext, inventory []BatchTargetIdentity) (*PreparedFailure, error) {
	entry := BatchPrepareContext{ReadPolicy: "agent_confirmed_current", DocumentRevision: trusted.DocumentRevision, Targets: trusted.Targets, OperationRevisions: trusted.OperationRevisions}
	if err := validateConfirmedTargetContext(c, entry, inventory); err != nil {
		return nil, err
	}
	state := map[string]BatchTargetVersion{}
	for _, t := range trusted.Targets {
		state[t.TargetID] = t
	}
	n := 0
	for _, op := range c.Operations {
		n += len(op.Reads)
	}
	if n != len(trusted.QualifiedReads) {
		return nil, ErrInvalid
	}

	type readKey struct {
		op    string
		index int
	}
	supplied := map[readKey]BatchQualifiedRead{}
	byID := map[string]BatchOperation{}
	indices := map[string]int{}
	for i, op := range c.Operations {
		byID[op.ID] = op
		indices[op.ID] = i
	}
	for i, op := range c.Operations {
		if len(op.Reads) > 256 || len(op.Dependencies) > 256 {
			return nil, ErrInvalid
		}
		deps := map[string]bool{}
		for _, dep := range op.Dependencies {
			if !batchID(dep) || deps[dep] {
				return nil, ErrInvalid
			}
			deps[dep] = true
			if at, exists := indices[dep]; exists && at >= i {
				return nil, ErrInvalid
			}
		}
		if _, err := batchCounter(op.BaseRevision); err != nil {
			return nil, ErrInvalid
		}
	}
	for _, q := range trusted.QualifiedReads {
		op, exists := byID[q.OperationID]
		index, err := strconv.Atoi(q.ReadIndex)
		if !exists || err != nil || index < 0 || index > 255 || strconv.Itoa(index) != q.ReadIndex || index >= len(op.Reads) {
			return nil, ErrInvalid
		}
		key := readKey{op.ID, index}
		if _, exists := supplied[key]; exists {
			return nil, ErrInvalid
		}
		read := op.Reads[index]
		revision, err := batchCounter(read.Revision)
		if err != nil {
			return nil, ErrInvalid
		}
		var structure uint64
		if read.StructureRevision != nil {
			structure, err = batchCounter(*read.StructureRevision)
			if err != nil {
				return nil, ErrInvalid
			}
		}
		if q.TargetID != read.TargetID || q.ObservedRevision != read.Revision || !reflect.DeepEqual(q.ObservedStructureRevision, read.StructureRevision) {
			return nil, ErrInvalid
		}
		target, exists := state[read.TargetID]
		if exists {
			want := &BatchCausalEntry{Revision: target.Revision, StructureRevision: target.StructureRevision, Deleted: target.Deleted}
			if !reflect.DeepEqual(q.Entry, want) {
				return nil, ErrInvalid
			}
			if !target.Deleted && read.Revision == target.Revision && (read.StructureRevision == nil || *read.StructureRevision == target.StructureRevision) && !q.Covered {
				return nil, ErrInvalid
			}
		} else if q.Entry != nil || q.Covered {
			return nil, ErrInvalid
		}
		if q.Covered {
			current, _ := batchCounter(target.Revision)
			if target.Deleted || revision > current || (read.StructureRevision != nil && structure > revision) || (read.Revision == target.Revision && read.StructureRevision != nil && *read.StructureRevision != target.StructureRevision) {
				return nil, ErrInvalid
			}
		}
		supplied[key] = q
	}

	head, _ := batchCounter(trusted.DocumentRevision)
	for _, op := range c.Operations {
		base, _ := batchCounter(op.BaseRevision)
		if base > head {
			return &PreparedFailure{OperationID: op.ID, Reason: batchReason("precondition_failed")}, nil
		}
		seen := map[string]bool{}
		for index, read := range op.Reads {
			q, exists := supplied[readKey{op.ID, index}]
			if !exists {
				return nil, ErrInvalid
			}
			code := ""
			if !batchID(read.TargetID) || seen[read.TargetID] {
				return &PreparedFailure{OperationID: op.ID, Reason: batchReason("invalid_request")}, nil
			}
			seen[read.TargetID] = true
			target, exists := state[read.TargetID]
			if !exists {
				code = "invalid_request"
			} else if target.Deleted {
				code = "target_deleted"
			} else if !q.Covered {
				code = "precondition_failed"
			}
			if code != "" {
				return &PreparedFailure{OperationID: op.ID, Reason: batchReason(code)}, nil
			}
		}
	}
	return nil, nil
}

// validateCausalTargetCandidate independently checks the application's effects
// against exact original reads. Every earlier virtual writer of a read target,
// including zero-effect dependency connectors, must lie in immutable closure.
// The durable history part was qualified from SQL before invoking the engine.
func (s *BatchStore) validateCausalTargetCandidate(c BatchChange, trusted BatchCausalPrepareContext, candidate BatchTargetCandidate, validated []BatchTargetIdentity) ([]preparedTargetRow, error) {
	initial := make([]BatchTargetIdentity, 0, len(trusted.Targets))
	for _, t := range trusted.Targets {
		initial = append(initial, BatchTargetIdentity{TargetID: t.TargetID, Deleted: t.Deleted})
	}
	failure, err := causalTargetReadFailure(c, trusted, initial)
	if err != nil || failure != nil {
		return nil, ErrInvalid
	}
	limit := s.config.Targets.Limits
	if !validTargetInventory(candidate.Inventory, limit.Targets) || !validTargetInventory(validated, limit.Targets) || len(candidate.Effects) != len(c.Operations) {
		return nil, ErrInvalid
	}
	expected := map[string]bool{}
	for _, t := range validated {
		expected[t.TargetID] = t.Deleted
	}
	if len(expected) != len(candidate.Inventory) {
		return nil, ErrInvalid
	}
	for _, t := range candidate.Inventory {
		if deleted, ok := expected[t.TargetID]; !ok || deleted != t.Deleted {
			return nil, ErrInvalid
		}
	}
	state := map[string]BatchTargetVersion{}
	for _, t := range trusted.Targets {
		state[t.TargetID] = t
	}
	indices := map[string]int{}
	for i, op := range c.Operations {
		indices[op.ID] = i
	}
	writers := map[string][]string{}
	var out []preparedTargetRow
	for i, op := range c.Operations {
		closure := map[string]bool{}
		stack := append([]string(nil), op.Dependencies...)
		for len(stack) > 0 {
			dep := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if closure[dep] {
				continue
			}
			closure[dep] = true
			if index, internal := indices[dep]; internal {
				if index >= i {
					return nil, ErrInvalid
				}
				stack = append(stack, c.Operations[index].Dependencies...)
			}
		}
		for _, read := range op.Reads {
			t, exists := state[read.TargetID]
			if !exists || t.Deleted {
				return nil, ErrInvalid
			}
			for _, writer := range writers[read.TargetID] {
				if !closure[writer] {
					return nil, ErrInvalid
				}
			}
		}
		effects := candidate.Effects[i]
		if effects.OperationID != op.ID || effects.Targets == nil || len(effects.Targets) > limit.EffectsPerOperation {
			return nil, ErrInvalid
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
			writers[effect.TargetID] = append(writers[effect.TargetID], op.ID)
			out = append(out, row)
		}
	}
	if len(state) != len(expected) {
		return nil, ErrInvalid
	}
	for id, t := range state {
		if deleted, ok := expected[id]; !ok || deleted != t.Deleted {
			return nil, ErrInvalid
		}
	}
	return out, nil
}
