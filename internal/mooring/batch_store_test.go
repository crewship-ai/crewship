package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type batchTestAdapter struct {
	apply func(context.Context, []byte, BatchChange) ([]byte, *BatchRejection, error)
	calls atomic.Int64
}

func (a *batchTestAdapter) ValidateCheckpoint(_ context.Context, b []byte) error {
	var n int
	return json.Unmarshal(b, &n)
}
func (a *batchTestAdapter) Apply(ctx context.Context, b []byte, c BatchChange) ([]byte, *BatchRejection, error) {
	a.calls.Add(1)
	if a.apply != nil {
		return a.apply(ctx, b, c)
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return nil, nil, err
	}
	return []byte(strconv.Itoa(n + len(c.Operations))), nil, nil
}
func batchFixture(t *testing.T) (*BatchStore, *batchTestAdapter, *OwnerStore, *ownerTestClock, OwnerLease) {
	t.Helper()
	owners, db, clock, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &batchTestAdapter{}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Adapter: adapter, Limits: DefaultBatchLimits()})
	if err := store.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatal(err)
	}
	return store, adapter, owners, clock, lease
}
func testBatchChange(t *testing.T, key SessionKey, id, task string, ops ...BatchOperation) BatchChange {
	t.Helper()
	c := BatchChange{ID: id, TaskID: task, Operations: ops}
	pre := batchPreimage{Domain: "mooring.atomic.v0.4", Scope: batchScope{key.ApplicationID, key.ArtifactID, key.WorkspaceID}, ID: id, TaskID: task, Operations: []batchImmutableOperation{}}
	for _, op := range ops {
		pre.Operations = append(pre.Operations, batchImmutableOperation{op.ID, op.Participant, op.BaseRevision, op.Dependencies, op.Reads, op.Payload})
	}
	raw, err := json.Marshal(pre)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := FingerprintAtomicJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.MembershipHash = hashes.MembershipHash
	for i := range c.Operations {
		c.Operations[i].Fingerprint = hashes.Operations[c.Operations[i].ID]
	}
	return c
}
func testBatchOp(id string, deps ...string) BatchOperation {
	if deps == nil {
		deps = []string{}
	}
	return BatchOperation{ID: id, Participant: "person", BaseRevision: "0", Dependencies: deps, Reads: []BatchReadPrecondition{}, Payload: json.RawMessage(`{"value":9007199254740993}`)}
}
func batchCount(t *testing.T, s *BatchStore, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestBatchDefaultOff(t *testing.T) {
	s := NewBatchStore(nil, BatchStoreConfig{})
	if _, err := s.Commit(t.Context(), BatchRequest{}); !errors.Is(err, ErrBatchDisabled) {
		t.Fatal(err)
	}
}

func TestBatchAtomicSavedRetryAndStatus(t *testing.T) {
	s, adapter, _, clock, lease := batchFixture(t)
	c := testBatchChange(t, lease.Key, "c", "", testBatchOp("a"), testBatchOp("b", "a"))
	events := 0
	s.config.AfterCommit = func(event BatchCommitEvent) {
		events++
		if event.Result.Revision != "2" {
			t.Error("wrong committed revision")
		}
		if batchCount(t, s, "mooring_collab_operations") != 2 {
			t.Error("callback before SQL commit")
		}
	}
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != "2" || result.Changes[0].FirstRevision != "1" || result.Changes[0].LastRevision != "2" || events != 1 {
		t.Fatalf("bad atomic result: %+v events%d", result, events)
	}
	var revisions string
	if err = s.db.QueryRow(`SELECT group_concat(revision,',') FROM mooring_collab_checkpoints`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != "0,2" {
		t.Fatal("published intermediate atomic checkpoint: ", revisions)
	}
	clock.advance(10 * time.Second)
	s.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
		if a.Intent == "write" {
			return &BatchDenied{"permission_revoked"}
		}
		return nil
	}
	retry, err := s.Commit(t.Context(), BatchRequest{lease, "999", []BatchChange{c}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(result)
	b, _ := json.Marshal(retry)
	if string(a) != string(b) || adapter.calls.Load() != 1 || events != 1 {
		t.Fatal("immutable retry reapplied or changed")
	}
	saved, err := s.Status(t.Context(), lease.Key, "a", c.Operations[0].Fingerprint)
	if err != nil || saved == nil || saved.Status != "saved" {
		t.Fatalf("status: %+v %v", saved, err)
	}
	if _, err = s.Status(t.Context(), lease.Key, "a", "sha256:"+fmt.Sprintf("%064d", 0)); !errors.Is(err, ErrOperationConflict) {
		t.Fatal("status mismatch not rejected", err)
	}
	unknown, err := s.Status(t.Context(), lease.Key, "unseen", c.Operations[0].Fingerprint)
	if err != nil || unknown != nil {
		t.Fatal("unknown not preserved")
	}
	deny := errors.New("scope denied")
	s.config.Authority = func(context.Context, *sql.Tx, BatchAccess) error { return deny }
	for _, id := range []string{"a", "unseen"} {
		value, err := s.Status(t.Context(), lease.Key, id, c.Operations[0].Fingerprint)
		if !errors.Is(err, deny) || value != nil {
			t.Fatal("existence leaked before authorization")
		}
	}
}

func TestBatchRevocationExpiryAndStorageFailure(t *testing.T) {
	for _, mode := range []string{"revoked", "owner_expired", "storage_failure", "engine_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, adapter, _, clock, lease := batchFixture(t)
			c := testBatchChange(t, lease.Key, "c", "", testBatchOp("a"))
			emitted := 0
			s.config.AfterCommit = func(BatchCommitEvent) { emitted++ }
			revoked := false
			s.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
				if a.Intent == "write" && revoked {
					return &BatchDenied{"permission_revoked"}
				}
				return nil
			}
			adapter.apply = func(_ context.Context, _ []byte, _ BatchChange) ([]byte, *BatchRejection, error) {
				if mode == "revoked" {
					revoked = true
				}
				if mode == "owner_expired" {
					clock.advance(6 * time.Second)
				}
				if mode == "engine_unknown" {
					return nil, nil, errors.New("engine outcome unknown")
				}
				return []byte("1"), nil, nil
			}
			if mode == "storage_failure" {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_collab_write BEFORE INSERT ON mooring_collab_operations BEGIN SELECT RAISE(ABORT,'injected disk failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}})
			if mode == "revoked" {
				if err != nil || result.Changes[0].Status != "rejected" || result.Changes[0].Operations[0].Reason.Code != "permission_revoked" || emitted != 1 {
					t.Fatalf("revocation: %+v %v", result, err)
				}
			} else {
				if err == nil || len(result.Changes) != 0 || emitted != 0 || batchCount(t, s, "mooring_collab_operations") != 0 {
					t.Fatalf("false terminal outcome: %+v %v", result, err)
				}
			}
			revision, checkpoint, readErr := s.ReadCheckpoint(t.Context(), lease.Key)
			if readErr != nil || revision != "0" || string(checkpoint) != "0" {
				t.Fatalf("uncommitted content exposed: %s %s %v", revision, checkpoint, readErr)
			}
		})
	}
}

func TestBatchPartialDependenciesAndImmutableAdapterInput(t *testing.T) {
	s, adapter, _, _, lease := batchFixture(t)
	s.config.Authority = func(_ context.Context, _ *sql.Tx, access BatchAccess) error {
		if access.Operation != nil {
			access.Operation.ID = "mutated-by-authority"
			access.Operation.Participant = "other"
		}
		return nil
	}
	a := testBatchChange(t, lease.Key, "a", "", testBatchOp("a1"), testBatchOp("a2", "a1"))
	b := testBatchChange(t, lease.Key, "b", "", testBatchOp("b1", "a2"))
	c := testBatchChange(t, lease.Key, "c", "", testBatchOp("c1", "b1"))
	independent := testBatchChange(t, lease.Key, "independent", "", testBatchOp("i"))
	adapter.apply = func(_ context.Context, state []byte, c BatchChange) ([]byte, *BatchRejection, error) {
		if c.ID == "a" {
			return nil, batchReason("target_deleted"), nil
		}
		if string(c.Operations[0].Payload) != `{"value":9007199254740992}` {
			t.Error("adapter saw original unnormalized payload")
		}
		c.Operations[0].Participant = "laundered"
		c.Operations[0].ID = "different"
		c.Operations[0].Payload[0] = '['
		return []byte("1"), nil, nil
	}
	// Reversed dependent input must sort whole changes while preserving root cause.
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c, b, a, independent}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != "1" || result.Changes[3].Status != "saved" || result.Changes[3].Operations[0].ID != "i" {
		t.Fatalf("independent work not retained: %+v", result)
	}
	for _, index := range []int{0, 1} {
		reason := result.Changes[index].Operations[0].Reason
		if reason.Code != "dependency_rejected" || reason.RootOperationID != "a2" || reason.RootCause.Code != "target_deleted" {
			t.Fatalf("lost original root: %+v", reason)
		}
	}
	// A historical rejected change's unknown dependency may appear in a later
	// batch. Its immutable old graph must not manufacture a new cycle.
	old := testBatchChange(t, lease.Key, "old", "", testBatchOp("old", "future"))
	first, err := s.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{old}})
	if err != nil || first.Changes[0].Operations[0].Reason.Code != "unknown_dependency" {
		t.Fatalf("unknown dependency %+v %v", first, err)
	}
	future := testBatchChange(t, lease.Key, "future", "", testBatchOp("future", "old"))
	retried, err := s.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{old, future}})
	if err != nil || retried.Changes[1].Operations[0].Reason.Code != "dependency_rejected" || retried.Changes[1].Operations[0].Reason.RootCause.Code != "unknown_dependency" {
		t.Fatalf("known outcome revalidated as new cycle %+v %v", retried, err)
	}
	var actor string
	if err = s.db.QueryRow(`SELECT participant_ref FROM mooring_collab_operations WHERE op_id='i'`).Scan(&actor); err != nil || actor != "person" {
		t.Fatal("adapter changed immutable identity", actor, err)
	}
}

func TestBatchTasksQuotasAndForwardDependencies(t *testing.T) {
	s, _, _, _, lease := batchFixture(t)
	unknown := testBatchChange(t, lease.Key, "unknown", "missing", testBatchOp("unknown"))
	if _, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{unknown}}); err == nil || batchCount(t, s, "mooring_collab_tasks") != 0 {
		t.Fatal("unknown task silently admitted")
	}
	if err := s.RegisterTask(t.Context(), lease, "task", "person", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterTask(t.Context(), lease, "task", "other", false); !errors.Is(err, ErrOperationConflict) {
		t.Fatal("task author reassigned", err)
	}
	if err := s.CancelTask(t.Context(), lease, "task"); err != nil {
		t.Fatal(err)
	}
	task := testBatchChange(t, lease.Key, "task-change", "task", testBatchOp("task-op"))
	result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{task}})
	if err != nil || result.Changes[0].Operations[0].Reason.Code != "task_cancelled" {
		t.Fatalf("cancelled task wrote: %+v %v", result, err)
	}
	for _, tc := range []struct {
		id   string
		ops  []BatchOperation
		code string
	}{
		{"forward", []BatchOperation{testBatchOp("f1", "f2"), testBatchOp("f2")}, "invalid_request"},
		{"cycle", []BatchOperation{testBatchOp("x1", "x2"), testBatchOp("x2", "x1")}, "dependency_cycle"},
		{"self", []BatchOperation{testBatchOp("self", "self")}, "dependency_cycle"},
	} {
		c := testBatchChange(t, lease.Key, tc.id, "", tc.ops...)
		result, err = s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}})
		if err != nil || result.Changes[0].Operations[0].Reason.Code != tc.code {
			t.Fatalf("%s %+v %v", tc.id, result, err)
		}
	}
	s.config.Limits.Checkpoints = 1
	quota := testBatchChange(t, lease.Key, "quota", "", testBatchOp("quota"))
	result, err = s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{quota}})
	if err != nil || result.Changes[0].Operations[0].Reason.Code != "quota_exceeded" {
		t.Fatalf("quota not durable: %+v %v", result, err)
	}
	s.config.Limits.LedgerOperations = batchCount(t, s, "mooring_collab_operations")
	full := testBatchChange(t, lease.Key, "full", "", testBatchOp("full"))
	result, err = s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{full}})
	if !errors.Is(err, ErrBatchCapacity) || len(result.Changes) != 0 {
		t.Fatalf("ledger forgot IDs: %+v %v", result, err)
	}
	saved, err := s.Status(t.Context(), lease.Key, "quota", quota.Operations[0].Fingerprint)
	if err != nil || saved == nil || saved.Status != "rejected" {
		t.Fatal("old immutable outcome lost", err)
	}
}

func TestBatchConcurrentCASAndUint64Ceiling(t *testing.T) {
	s, _, _, _, lease := batchFixture(t)
	results := make(chan BatchResult, 2)
	errs := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			// Prepare before goroutine using no t.Fatal path: fixed valid fixture input.
			op := testBatchOp(id)
			pre := batchPreimage{Domain: "mooring.atomic.v0.4", Scope: batchScope{lease.Key.ApplicationID, lease.Key.ArtifactID, lease.Key.WorkspaceID}, ID: id, Operations: []batchImmutableOperation{{op.ID, op.Participant, op.BaseRevision, op.Dependencies, op.Reads, op.Payload}}}
			raw, _ := json.Marshal(pre)
			hashes, e := FingerprintAtomicJSON(raw)
			if e != nil {
				errs <- e
				return
			}
			op.Fingerprint = hashes.Operations[id]
			result, e := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{{ID: id, MembershipHash: hashes.MembershipHash, Operations: []BatchOperation{op}}}})
			if e != nil {
				errs <- e
				return
			}
			results <- result
		}(id)
	}
	saved, rejected := 0, 0
	for range 2 {
		select {
		case err := <-errs:
			t.Fatal(err)
		case result := <-results:
			if result.Changes[0].Status == "saved" {
				saved++
			} else if result.Changes[0].Operations[0].Reason.Code == "precondition_failed" {
				rejected++
			}
		}
	}
	if saved != 1 || rejected != 1 {
		t.Fatalf("writer serialization/CAS failed saved%d rejected%d", saved, rejected)
	}
	// Synthesize a legitimate boundary checkpoint without iterating 2^64 writes.
	if _, err := s.db.Exec(`UPDATE mooring_collab_documents SET revision='18446744073709551614'`); err != nil {
		t.Fatal(err)
	}
	overflow := testBatchChange(t, lease.Key, "overflow", "", testBatchOp("over1"), testBatchOp("over2"))
	result, err := s.Commit(t.Context(), BatchRequest{lease, "18446744073709551614", []BatchChange{overflow}})
	if err != nil || result.Changes[0].Operations[0].Reason.Code != "capacity_exhausted" {
		t.Fatalf("uint64 overflow %+v %v", result, err)
	}
	last := testBatchChange(t, lease.Key, "last", "", testBatchOp("last"))
	result, err = s.Commit(t.Context(), BatchRequest{lease, "18446744073709551614", []BatchChange{last}})
	if err != nil || result.Revision != "18446744073709551615" {
		t.Fatalf("uint64 ceiling not exact %+v %v", result, err)
	}
}
