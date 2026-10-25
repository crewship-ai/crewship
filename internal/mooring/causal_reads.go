package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
)

// These pure preparation values are NOT accepted by the current strict private
// profile. A future explicitly negotiated causal dispatcher must bind them to
// its original request/scope/checkpoint/change membership. They grant no authority.
type BatchCausalEntry struct {
	Revision          string `json:"revision"`
	StructureRevision string `json:"structure_revision"`
	Deleted           bool   `json:"deleted"`
}
type BatchQualifiedRead struct {
	OperationID               string            `json:"op_id"`
	ReadIndex                 string            `json:"read_index"`
	TargetID                  string            `json:"target_id"`
	ObservedRevision          string            `json:"observed_revision"`
	ObservedStructureRevision *string           `json:"observed_structure_revision,omitempty"`
	Entry                     *BatchCausalEntry `json:"entry"`
	Covered                   bool              `json:"covered"`
}
type BatchCausalPrepareContext struct {
	DocumentRevision   string                   `json:"document_revision"`
	Targets            []BatchTargetVersion     `json:"targets"`
	OperationRevisions []BatchOperationRevision `json:"operation_revisions"`
	QualifiedReads     []BatchQualifiedRead     `json:"qualified_reads"`
}
type BatchHistoryBudget struct{ Operations, DependencyEdges, Effects int }

func DefaultBatchHistoryBudget() BatchHistoryBudget { return BatchHistoryBudget{10000, 65536, 100000} }

type causalOperation struct {
	revision       uint64
	dependencies   []string
	effects        []preparedTargetRow
	effectByTarget map[string]int
}
type causalHistory struct {
	states  map[string]BatchTargetVersion
	effects []preparedTargetRow
}
type causalReader struct {
	ctx                  context.Context
	tx                   *sql.Tx
	key                  SessionKey
	head                 uint64
	budget               BatchHistoryBudget
	nodes                map[string]*causalOperation
	histories            map[string]*causalHistory
	virtual              map[string]int
	change               BatchChange
	edges, effects, work int
	bytes                int64
	byteLimit            int64
}

// Bound repeated traversals as well as unique rows. A 1k chain across a full
// 256-member batch fits; capacity exhaustion never means an uncovered read.
const causalWorkLimit = 1000000

func (r *causalReader) chargeWork() error {
	r.work++
	if r.work > causalWorkLimit {
		return ErrBatchCapacity
	}
	return nil
}
func (r *causalReader) chargeBytes(n int64) error {
	if n < 0 || n > r.byteLimit-r.bytes {
		return ErrBatchCapacity
	}
	r.bytes += n
	return nil
}

// qualifyCausalReads is an unused, default-off SQL history primitive. Caller must
// hold this store's writer transaction and supply the complete app-qualified
// inventory of the SAME current checkpoint. This function never authorizes,
// applies, installs, commits or issues a rejection/receipt. Rejected dependencies
// belong to the host planner BEFORE preparation; uncertain history returns error.
func (s *BatchStore) qualifyCausalReads(ctx context.Context, tx *sql.Tx, key SessionKey, change BatchChange, inventory []BatchTargetIdentity, budget BatchHistoryBudget) (BatchCausalPrepareContext, error) {
	var empty BatchCausalPrepareContext
	if !s.targetsEnabled() || !s.targetConfigValid() || budget.Operations < 1 || budget.DependencyEdges < 1 || budget.Effects < 1 || budget.Operations > 10000 || budget.DependencyEdges > 65536 || budget.Effects > 100000 {
		return empty, ErrInvalid
	}
	headText, _, err := batchScopeMatches(ctx, tx, key)
	if err != nil {
		return empty, err
	}
	if err = s.requireTargetMode(ctx, tx, key.ArtifactID); err != nil {
		return empty, err
	}
	head, err := batchCounter(headText)
	if err != nil {
		return empty, err
	}
	entry, err := s.targetContext(ctx, tx, key, change, head)
	if err != nil {
		return empty, err
	}
	if err = validateConfirmedTargetContext(change, entry, inventory); err != nil {
		return empty, err
	}
	r := causalReader{ctx: ctx, tx: tx, key: key, head: head, budget: budget, nodes: map[string]*causalOperation{}, histories: map[string]*causalHistory{}, virtual: map[string]int{}, change: change, byteLimit: s.config.Targets.Limits.ProvenanceBytes}
	if r.byteLimit > 128<<20 {
		r.byteLimit = 128 << 20
	}
	for i, op := range change.Operations {
		var existing int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_operations WHERE page_id=? AND op_id=?)`, key.ArtifactID, op.ID).Scan(&existing); err != nil {
			return empty, err
		}
		if existing != 0 {
			return empty, ErrOperationConflict
		}
		r.virtual[op.ID] = i
		if len(op.Dependencies) > 256 {
			return empty, ErrInvalid
		}
		deps := map[string]bool{}
		for _, dep := range op.Dependencies {
			if !batchID(dep) || deps[dep] || dep == op.ID {
				return empty, ErrInvalid
			}
			deps[dep] = true
		}
		if len(op.Dependencies) > budget.DependencyEdges-r.edges {
			return empty, ErrBatchCapacity
		}
		r.edges += len(op.Dependencies)
	}
	targets := map[string]BatchTargetVersion{}
	for _, target := range entry.Targets {
		targets[target.TargetID] = target
	}
	result := BatchCausalPrepareContext{entry.DocumentRevision, entry.Targets, entry.OperationRevisions, []BatchQualifiedRead{}}
	for index, op := range change.Operations {
		if len(op.Reads) > 256 || len(op.Dependencies) > 256 {
			return empty, ErrInvalid
		}
		if _, err = batchCounter(op.BaseRevision); err != nil {
			return empty, err
		}
		covered, err := r.closure(index)
		if err != nil {
			return empty, err
		}
		seen := map[string]bool{}
		for readIndex, read := range op.Reads {
			if !batchID(read.TargetID) || seen[read.TargetID] {
				return empty, ErrInvalid
			}
			seen[read.TargetID] = true
			if _, err = batchCounter(read.Revision); err != nil {
				return empty, err
			}
			q := BatchQualifiedRead{OperationID: op.ID, ReadIndex: strconv.Itoa(readIndex), TargetID: read.TargetID, ObservedRevision: read.Revision}
			if read.StructureRevision != nil {
				if _, err = batchCounter(*read.StructureRevision); err != nil {
					return empty, err
				}
				v := *read.StructureRevision
				q.ObservedStructureRevision = &v
			}
			target, exists := targets[read.TargetID]
			if exists {
				q.Entry = &BatchCausalEntry{target.Revision, target.StructureRevision, target.Deleted}
				history, err := r.history(target)
				if err != nil {
					return empty, err
				}
				observed, exists := history.states[read.Revision]
				q.Covered = exists && !observed.Deleted && !target.Deleted && (read.StructureRevision == nil || *read.StructureRevision == observed.StructureRevision)
				if q.Covered {
					base, _ := batchCounter(read.Revision)
					for _, effect := range history.effects {
						if err = r.chargeWork(); err != nil {
							return empty, err
						}
						revision, _ := batchCounter(effect.Revision)
						if revision > base && !covered[effect.OperationID] {
							q.Covered = false
						}
					}
				}
			}
			result.QualifiedReads = append(result.QualifiedReads, q)
		}
	}
	return result, nil
}

func (r *causalReader) closure(index int) (map[string]bool, error) {
	type item struct {
		id     string
		parent uint64
	}
	label := r.head + uint64(index) + 1
	stack := []item{}
	for _, id := range r.change.Operations[index].Dependencies {
		stack = append(stack, item{id, label})
	}
	covered := map[string]bool{}
	for len(stack) > 0 {
		if err := r.chargeWork(); err != nil {
			return nil, err
		}
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !batchID(node.id) {
			return nil, ErrInvalid
		}
		if i, ok := r.virtual[node.id]; ok {
			revision := r.head + uint64(i) + 1
			if i >= index || revision >= node.parent {
				return nil, ErrInvalid
			}
			if covered[node.id] {
				continue
			}
			covered[node.id] = true
			for _, dep := range r.change.Operations[i].Dependencies {
				stack = append(stack, item{dep, revision})
			}
			continue
		}
		saved, err := r.operation(node.id)
		if err != nil {
			return nil, err
		}
		if saved.revision >= node.parent {
			return nil, ErrInvalid
		}
		if covered[node.id] {
			continue
		}
		covered[node.id] = true
		for _, dep := range saved.dependencies {
			stack = append(stack, item{dep, saved.revision})
		}
	}
	return covered, nil
}

func (r *causalReader) operation(id string) (*causalOperation, error) {
	if node, ok := r.nodes[id]; ok {
		return node, nil
	}
	if len(r.nodes) >= r.budget.Operations {
		return nil, ErrBatchCapacity
	}
	var workspace, resultJSON, depsJSON string
	var complete int
	var resultBytes, depsBytes int64
	err := r.tx.QueryRowContext(r.ctx, `SELECT workspace_id,length(CAST(result_json AS BLOB)),length(CAST(dependencies_json AS BLOB)),target_effects_complete FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, r.key.ArtifactID, id).Scan(&workspace, &resultBytes, &depsBytes, &complete)
	if err != nil {
		return nil, err
	}
	if workspace != r.key.WorkspaceID || complete != 1 || resultBytes > 8192 || depsBytes > 262144 {
		return nil, ErrInvalid
	}
	if err = r.chargeBytes(resultBytes); err != nil {
		return nil, err
	}
	if err = r.chargeBytes(depsBytes); err != nil {
		return nil, err
	}
	if err = r.tx.QueryRowContext(r.ctx, `SELECT result_json,dependencies_json FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, r.key.ArtifactID, id).Scan(&resultJSON, &depsJSON); err != nil {
		return nil, err
	}
	var outcome BatchOperationResult
	var deps []string
	if json.Unmarshal([]byte(resultJSON), &outcome) != nil || !validStoredBatchOperation(outcome) || outcome.ID != id || outcome.Status != "saved" || json.Unmarshal([]byte(depsJSON), &deps) != nil || deps == nil || len(deps) > 256 {
		return nil, ErrInvalid
	}
	roundOutcome, _ := json.Marshal(outcome)
	roundDeps, _ := json.Marshal(deps)
	if !bytes.Equal(roundOutcome, []byte(resultJSON)) || !bytes.Equal(roundDeps, []byte(depsJSON)) {
		return nil, ErrInvalid
	}
	revision, err := batchCounter(outcome.Revision)
	if err != nil || revision == 0 || revision > r.head {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, dep := range deps {
		if !batchID(dep) || seen[dep] || dep == id {
			return nil, ErrInvalid
		}
		seen[dep] = true
	}
	if len(deps) > r.budget.DependencyEdges-r.edges {
		return nil, ErrBatchCapacity
	}
	r.edges += len(deps)
	var effectCount int
	var effectBytes, maxRecord int64
	if err = r.tx.QueryRowContext(r.ctx, `SELECT count(*),COALESCE(sum(length(CAST(record_json AS BLOB))),0),COALESCE(max(length(CAST(record_json AS BLOB))),0) FROM mooring_collab_target_effects WHERE page_id=? AND op_id=?`, r.key.ArtifactID, id).Scan(&effectCount, &effectBytes, &maxRecord); err != nil {
		return nil, err
	}
	if effectCount > 4096 || maxRecord > 8192 {
		return nil, ErrInvalid
	}
	if effectCount > r.budget.Effects-r.effects {
		return nil, ErrBatchCapacity
	}
	if err = r.chargeBytes(effectBytes); err != nil {
		return nil, err
	}
	rows, err := r.tx.QueryContext(r.ctx, `SELECT workspace_id,target_id,revision,before_revision,before_structure_revision,content_changed,structure_changed,created,deleted,record_json FROM mooring_collab_target_effects WHERE page_id=? AND op_id=? ORDER BY target_id`, r.key.ArtifactID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	node := &causalOperation{revision: revision, dependencies: deps, effects: []preparedTargetRow{}, effectByTarget: map[string]int{}}
	for rows.Next() {
		if r.effects >= r.budget.Effects {
			return nil, ErrBatchCapacity
		}
		r.effects++
		var row preparedTargetRow
		var before, structure sql.NullString
		var raw string
		row.OperationID = id
		if err = rows.Scan(&workspace, &row.Effect.TargetID, &row.Revision, &before, &structure, &row.Effect.ContentChanged, &row.Effect.StructureChanged, &row.Effect.Created, &row.Effect.Deleted, &raw); err != nil {
			return nil, err
		}
		if workspace != r.key.WorkspaceID || !batchID(row.Effect.TargetID) || row.Revision != outcome.Revision || before.Valid != structure.Valid || row.Effect.Created == before.Valid || (!row.Effect.ContentChanged && !row.Effect.StructureChanged) || (row.Effect.Created && row.Effect.Deleted) || ((row.Effect.Created || row.Effect.Deleted) && !row.Effect.StructureChanged) {
			return nil, ErrInvalid
		}
		if before.Valid {
			b, e := batchCounter(before.String)
			if e != nil {
				return nil, e
			}
			st, e := batchCounter(structure.String)
			if e != nil || st > b || b >= revision {
				return nil, ErrInvalid
			}
			row.Before = &BatchTargetVersion{TargetID: row.Effect.TargetID, Revision: before.String, StructureRevision: structure.String}
		}
		row.After = BatchTargetVersion{TargetID: row.Effect.TargetID, Revision: row.Revision, Deleted: row.Effect.Deleted}
		if row.Effect.StructureChanged {
			row.After.StructureRevision = row.Revision
		} else if row.Before != nil {
			row.After.StructureRevision = row.Before.StructureRevision
		} else {
			return nil, ErrInvalid
		}
		expected, e := targetEffectRecord(r.key, row)
		if e != nil || !bytes.Equal(expected, []byte(raw)) {
			return nil, ErrInvalid
		}
		node.effectByTarget[row.Effect.TargetID] = len(node.effects)
		node.effects = append(node.effects, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	r.nodes[id] = node
	return node, nil
}

func (r *causalReader) history(target BatchTargetVersion) (*causalHistory, error) {
	if history, ok := r.histories[target.TargetID]; ok {
		return history, nil
	}
	rows, err := r.tx.QueryContext(r.ctx, `SELECT op_id FROM mooring_collab_target_effects WHERE page_id=? AND target_id=? ORDER BY length(revision),revision COLLATE BINARY LIMIT ?`, r.key.ArtifactID, target.TargetID, r.budget.Effects+1)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		if len(ids) > r.budget.Effects {
			_ = rows.Close()
			return nil, ErrBatchCapacity
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	history := &causalHistory{states: map[string]BatchTargetVersion{}, effects: []preparedTargetRow{}}
	var state *BatchTargetVersion
	if len(ids) == 0 {
		if target.Revision != "0" || target.StructureRevision != "0" {
			return nil, ErrInvalid
		}
		history.states["0"] = target
		r.histories[target.TargetID] = history
		return history, nil
	}
	for _, id := range ids {
		if err = r.chargeWork(); err != nil {
			return nil, err
		}
		node, err := r.operation(id)
		if err != nil {
			return nil, err
		}
		effectIndex, exists := node.effectByTarget[target.TargetID]
		if !exists {
			return nil, ErrInvalid
		}
		effect := &node.effects[effectIndex]
		if state == nil {
			if effect.Before != nil {
				if effect.Before.Revision != "0" || effect.Before.StructureRevision != "0" {
					return nil, ErrInvalid
				}
				initial := *effect.Before
				state = &initial
				history.states["0"] = initial
			}
		}
		if !reflect.DeepEqual(state, effect.Before) {
			return nil, ErrInvalid
		}
		if state != nil && state.Deleted {
			return nil, ErrInvalid
		}
		next := effect.After
		state = &next
		history.states[next.Revision] = next
		history.effects = append(history.effects, *effect)
	}
	if state == nil || !reflect.DeepEqual(*state, target) {
		return nil, ErrInvalid
	}
	r.histories[target.TargetID] = history
	return history, nil
}
