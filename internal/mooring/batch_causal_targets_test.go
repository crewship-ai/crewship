package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type causalExecutionAdapter struct {
	targetTestAdapter
	causalCalls  int
	seen         []BatchChange
	contexts     []BatchCausalPrepareContext
	mutateCausal func(*BatchTargetCandidate)
}

func (a *causalExecutionAdapter) PrepareCausalTargets(_ context.Context, _ []byte, c BatchChange, q BatchCausalPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	a.causalCalls++
	a.seen = append(a.seen, cloneBatchChange(c))
	a.contexts = append(a.contexts, q)
	out := BatchTargetCandidate{Checkpoint: []byte("1"), Inventory: []BatchTargetIdentity{{TargetID: "p"}}, Effects: []BatchOperationEffects{}}
	for _, op := range c.Operations {
		out.Effects = append(out.Effects, BatchOperationEffects{OperationID: op.ID, Targets: []BatchTargetEffect{{TargetID: "p", ContentChanged: true}}})
	}
	if a.mutateCausal != nil {
		a.mutateCausal(&out)
	}
	return out, nil, nil
}
func enableCausalExecution(s *BatchStore, a *causalExecutionAdapter) {
	s.config.Targets.Adapter = a
	s.config.Targets.ReadPolicy = CausalTargetReadPolicy
	s.config.Targets.HistoryBudget = DefaultBatchHistoryBudget()
}
func TestCausalTargetsActualSQLCommitOriginalReadsAndEveryEffect(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	first := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
	if result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{first}}); err != nil || result.Changes[0].Status != "saved" {
		t.Fatal(result, err)
	}
	b := targetReadOp("b", "0")
	b.Dependencies = []string{"a"}
	second := testBatchChange(t, lease.Key, "change-b", "", b)
	original, _ := json.Marshal(second)
	request := BatchRequest{lease, "1", []BatchChange{second}}
	result, err := s.Commit(t.Context(), request)
	if err != nil || result.Revision != "2" || result.Changes[0].Status != "saved" || a.causalCalls != 2 || a.calls != 0 {
		t.Fatal(result, err, a.causalCalls, a.calls)
	}
	after, _ := json.Marshal(second)
	if !reflect.DeepEqual(original, after) || a.seen[1].Operations[0].Reads[0].Revision != "0" || a.contexts[1].QualifiedReads[0].Entry.Revision != "1" || !a.contexts[1].QualifiedReads[0].Covered {
		t.Fatal("original immutable read rewritten", string(after), a.contexts)
	}
	if _, err := s.Commit(t.Context(), request); err != nil || a.causalCalls != 2 {
		t.Fatal("exact replay reentered engine", err, a.causalCalls)
	}
	// An unrelated intervening effect cannot be hidden by a later covered effect.
	unrelated := testBatchChange(t, lease.Key, "unrelated-change", "", targetReadOp("unrelated", "2"))
	if r, e := s.Commit(t.Context(), BatchRequest{lease, "2", []BatchChange{unrelated}}); e != nil || r.Revision != "3" {
		t.Fatal(r, e)
	}
	stale := targetReadOp("stale", "0")
	stale.Dependencies = []string{"b"}
	denied := testBatchChange(t, lease.Key, "denied-change", "", stale)
	result, err = s.Commit(t.Context(), BatchRequest{lease, "3", []BatchChange{denied}})
	if err != nil || result.Changes[0].Status != "rejected" || result.Changes[0].Operations[0].Reason.Code != "precondition_failed" || a.causalCalls != 3 {
		t.Fatal(result, err, a.causalCalls)
	}
}
func TestCausalTargetsHostRejectsApplicationPrefixBypassAndUncertainHistory(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := &causalExecutionAdapter{}
	enableCausalExecution(s, a)
	first := targetReadOp("a", "0")
	second := targetReadOp("b", "0")
	bad := testBatchChange(t, lease.Key, "uncovered-prefix", "", first, second)
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{bad}})
	if !errors.Is(err, ErrInvalid) || len(result.Changes) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
		t.Fatal("host accepted malicious app bypass", result, err)
	}
	second.Dependencies = []string{"a"}
	good := testBatchChange(t, lease.Key, "covered-prefix", "", first, second)
	if r, e := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{good}}); e != nil || r.Revision != "2" {
		t.Fatal(r, e)
	}
	if _, err = s.db.Exec(`UPDATE mooring_collab_operations SET target_effects_complete=0 WHERE op_id='a'`); err != nil {
		t.Fatal(err)
	}
	probe := targetReadOp("probe", "0")
	probe.Dependencies = []string{"b"}
	calls := a.causalCalls
	result, err = s.Commit(t.Context(), BatchRequest{lease, "2", []BatchChange{testBatchChange(t, lease.Key, "missing-history", "", probe)}})
	if err == nil || len(result.Changes) != 0 || a.causalCalls != calls || batchCount(t, s, "mooring_collab_operations") != 2 {
		t.Fatal("missing history issued terminal outcome", result, err, a.causalCalls)
	}
}
func TestCausalTargetsConfigurationCannotSilentlyFallBack(t *testing.T) {
	s := &BatchStore{config: BatchStoreConfig{Targets: &TargetStoreConfig{Enabled: true, Adapter: &targetTestAdapter{}, Limits: DefaultBatchTargetLimits()}}}
	s.config.Targets.ReadPolicy = CausalTargetReadPolicy
	s.config.Targets.HistoryBudget = DefaultBatchHistoryBudget()
	if s.targetConfigValid() {
		t.Fatal("strict adapter granted causal capability")
	}
	s.config.Targets.Adapter = &causalExecutionAdapter{}
	if !s.targetConfigValid() {
		t.Fatal("explicit causal adapter denied")
	}
	s.config.Targets.ReadPolicy = "future_unqualified_policy"
	if s.targetConfigValid() {
		t.Fatal("unknown policy admitted")
	}
}

func TestCausalTargetsBindingsCompleteBeforeTerminalReadDecision(t *testing.T) {
	first := targetReadOp("a", "0")
	second := targetReadOp("b", "0")
	second.Dependencies = []string{"a"}
	change := BatchChange{Operations: []BatchOperation{first, second}}
	inventory := []BatchTargetIdentity{{TargetID: "p"}}
	context := BatchCausalPrepareContext{DocumentRevision: "1", Targets: []BatchTargetVersion{{TargetID: "p", Revision: "1", StructureRevision: "0"}}, OperationRevisions: []BatchOperationRevision{{OperationID: "a", Revision: "2"}, {OperationID: "b", Revision: "3"}}, QualifiedReads: []BatchQualifiedRead{{OperationID: "a", ReadIndex: "0", TargetID: "p", ObservedRevision: "0", Entry: &BatchCausalEntry{Revision: "1", StructureRevision: "0"}, Covered: false}, {OperationID: "b", ReadIndex: "0", TargetID: "p", ObservedRevision: "0", Entry: &BatchCausalEntry{Revision: "1", StructureRevision: "0"}, Covered: true}}}
	failure, err := causalTargetReadFailure(change, context, inventory)
	if err != nil || failure == nil || failure.OperationID != "a" || failure.Reason.Code != "precondition_failed" {
		t.Fatal(failure, err)
	}
	// A malformed later attestation must not persist an earlier read rejection.
	context.QualifiedReads[1].OperationID = "foreign"
	if f, e := causalTargetReadFailure(change, context, inventory); !errors.Is(e, ErrInvalid) || f != nil {
		t.Fatal("partial binding became terminal", f, e)
	}
	context.QualifiedReads[1].OperationID = "b"
	context.QualifiedReads[0].Covered = true
	context.QualifiedReads[0], context.QualifiedReads[1] = context.QualifiedReads[1], context.QualifiedReads[0]
	if f, e := causalTargetReadFailure(change, context, inventory); e != nil || f != nil {
		t.Fatal("member-index binding depends on array order", f, e)
	}
	context.QualifiedReads[0].ObservedRevision = "2"
	change.Operations[1].Reads[0].Revision = "2"
	if f, e := causalTargetReadFailure(change, context, inventory); !errors.Is(e, ErrInvalid) || f != nil {
		t.Fatal("host attested impossible future read", f, e)
	}
}
