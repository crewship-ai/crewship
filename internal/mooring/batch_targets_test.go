package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

type targetTestAdapter struct {
	mutate func(*BatchTargetCandidate, *BatchPrepareContext)
	calls  int
}

func (a *targetTestAdapter) ValidateTargets(_ context.Context, b []byte) ([]BatchTargetIdentity, error) {
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	return []BatchTargetIdentity{{TargetID: "p"}}, nil
}
func (a *targetTestAdapter) PrepareTargets(_ context.Context, b []byte, c BatchChange, ctx BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	a.calls++
	out := BatchTargetCandidate{Checkpoint: []byte("1"), Inventory: []BatchTargetIdentity{{TargetID: "p"}}, Effects: []BatchOperationEffects{}}
	for _, op := range c.Operations {
		out.Effects = append(out.Effects, BatchOperationEffects{OperationID: op.ID, Targets: []BatchTargetEffect{{TargetID: "p", ContentChanged: true}}})
	}
	if a.mutate != nil {
		a.mutate(&out, &ctx)
	}
	return out, nil, nil
}
func targetFixture(t *testing.T) (*BatchStore, *targetTestAdapter, OwnerLease) {
	t.Helper()
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "target-runtime")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &targetTestAdapter{}
	s := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, Limits: DefaultBatchTargetLimits()}})
	if err = s.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatal(err)
	}
	return s, adapter, lease
}
func targetReadOp(id, revision string) BatchOperation {
	op := testBatchOp(id)
	op.Reads = []BatchReadPrecondition{{TargetID: "p", Revision: revision}}
	return op
}
func TestBatchTargetsCommitProvenanceAndImmutableRetry(t *testing.T) {
	s, a, lease := targetFixture(t)
	c := testBatchChange(t, lease.Key, "target-change", "", targetReadOp("a", "0"))
	events := 0
	s.config.AfterCommit = func(BatchCommitEvent) {
		events++
		if batchCount(t, s, "mooring_collab_target_effects") != 1 {
			t.Error("event before effects commit")
		}
	}
	request := BatchRequest{lease, "0", []BatchChange{c}}
	r, err := s.Commit(t.Context(), request)
	if err != nil || r.Revision != "1" {
		t.Fatal(r, err)
	}
	var revision, structure, before string
	if err = s.db.QueryRow(`SELECT revision,structure_revision FROM mooring_collab_target_versions WHERE target_id='p'`).Scan(&revision, &structure); err != nil || revision != "1" || structure != "0" {
		t.Fatal(revision, structure, err)
	}
	if err = s.db.QueryRow(`SELECT before_revision FROM mooring_collab_target_effects WHERE op_id='a'`).Scan(&before); err != nil || before != "0" {
		t.Fatal(before, err)
	}
	var deps string
	var complete int
	if err = s.db.QueryRow(`SELECT dependencies_json,target_effects_complete FROM mooring_collab_operations WHERE op_id='a'`).Scan(&deps, &complete); err != nil || deps != "[]" || complete != 1 {
		t.Fatal("missing immutable no-history-gap provenance", deps, complete, err)
	}
	if _, err = s.Commit(t.Context(), request); err != nil || a.calls != 1 || events != 1 || batchCount(t, s, "mooring_collab_target_effects") != 1 {
		t.Fatal("retry changed effects", a.calls, events, err)
	}
}
func TestBatchTargetsUncertainBindingsAndStorageRollback(t *testing.T) {
	for _, mode := range []string{"foreign_op", "foreign_target", "missing_effect", "inventory_lie", "incomplete_context", "storage_failure", "tracking_disabled"} {
		t.Run(mode, func(t *testing.T) {
			s, a, lease := targetFixture(t)
			op := targetReadOp("a", "0")
			switch mode {
			case "foreign_op":
				a.mutate = func(c *BatchTargetCandidate, _ *BatchPrepareContext) { c.Effects[0].OperationID = "foreign" }
			case "foreign_target":
				a.mutate = func(c *BatchTargetCandidate, _ *BatchPrepareContext) { c.Effects[0].Targets[0].TargetID = "foreign" }
			case "missing_effect":
				a.mutate = func(c *BatchTargetCandidate, _ *BatchPrepareContext) { c.Effects = nil }
			case "inventory_lie":
				a.mutate = func(c *BatchTargetCandidate, _ *BatchPrepareContext) { c.Inventory = []BatchTargetIdentity{} }
			case "incomplete_context":
				if _, err := s.db.Exec(`DELETE FROM mooring_collab_target_versions`); err != nil {
					t.Fatal(err)
				}
			case "storage_failure":
				if _, err := s.db.Exec(`CREATE TRIGGER fail_target_effect BEFORE INSERT ON mooring_collab_target_effects BEGIN SELECT RAISE(ABORT,'synthetic target disk failure');END`); err != nil {
					t.Fatal(err)
				}
			case "tracking_disabled":
				s.config.Targets.Enabled = false
				s.config.Adapter = &batchTestAdapter{}
			}
			c := testBatchChange(t, lease.Key, "bad-target", "", op)
			events := 0
			s.config.AfterCommit = func(BatchCommitEvent) { events++ }
			result, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}})
			if err == nil || len(result.Changes) != 0 || events != 0 {
				t.Fatal("uncertain candidate emitted outcome", result, err, events)
			}
			if batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_target_effects") != 0 {
				t.Fatal("failed candidate retained ledger")
			}
			var revision string
			err = s.db.QueryRow(`SELECT revision FROM mooring_collab_target_versions WHERE target_id='p'`).Scan(&revision)
			if mode == "incomplete_context" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("corrupt missing inventory was not preserved", err)
				}
			} else if err != nil || revision != "0" {
				t.Fatal("target rollback", revision, err)
			}
		})
	}
}
func TestBatchTargetsStrictCurrentDoesNotPretendCausalReads(t *testing.T) {
	s, _, lease := targetFixture(t)
	a := targetReadOp("a", "0")
	b := targetReadOp("b", "0")
	b.Dependencies = []string{"a"}
	c := testBatchChange(t, lease.Key, "pending-causal", "", a, b)
	if _, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("stale causal observations silently ignored", err)
	}
	if batchCount(t, s, "mooring_collab_operations") != 0 {
		t.Fatal("staged unsupported capability saved")
	}
}
func TestBatchTargetsQuotaBeforePreparation(t *testing.T) {
	s, a, lease := targetFixture(t)
	s.config.Targets.Limits.Effects = 1
	s.config.Targets.Limits.EffectsPerOperation = 2
	c := testBatchChange(t, lease.Key, "capacity", "", targetReadOp("a", "0"))
	if _, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}}); !errors.Is(err, ErrBatchCapacity) || a.calls != 0 {
		t.Fatal("quota admitted app work", err, a.calls)
	}
}
func TestBatchTargetsCanonicalUnsignedStorage(t *testing.T) {
	s, _, lease := targetFixture(t)
	for _, bad := range []string{"", "01", "-1", "18446744073709551616", "9223372036854775808x", "1\x000"} {
		if _, err := s.db.Exec(`UPDATE mooring_collab_target_versions SET revision=? WHERE page_id=?`, bad, lease.Key.ArtifactID); err == nil {
			t.Fatal("noncanonical/out-of-range stored", bad)
		}
	}
	for _, valid := range []string{"9", "10", "9223372036854775808", "18446744073709551615"} {
		if _, err := s.db.Exec(`INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,?,?,'0',0)`, lease.Key.ArtifactID, lease.Key.WorkspaceID, "p"+valid, valid); err != nil {
			t.Fatal(valid, err)
		}
	}
	rows, err := s.db.Query(`SELECT revision FROM mooring_collab_target_versions WHERE target_id<>'p' ORDER BY length(revision),revision COLLATE BINARY`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var r string
		if err = rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"9", "10", "9223372036854775808", "18446744073709551615"}
	for i := range want {
		if len(got) != len(want) || got[i] != want[i] {
			t.Fatal(got)
		}
	}
}

func TestBatchTargetsConfirmedReadReasonsPreserveIndependentChange(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "structure"} {
		t.Run(mode, func(t *testing.T) {
			s, _, lease := targetFixture(t)
			op := targetReadOp("invalid-reader", "0")
			code := "precondition_failed"
			if mode == "missing" {
				op.Reads[0].TargetID = "missing"
				code = "invalid_request"
			}
			if mode == "stale" {
				op.Reads[0].Revision = "99"
			}
			if mode == "structure" {
				r := "99"
				op.Reads[0].StructureRevision = &r
			}
			bad := testBatchChange(t, lease.Key, "read-fails", "", op)
			good := testBatchChange(t, lease.Key, "independent", "", targetReadOp("independent-writer", "0"))
			r, err := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{bad, good}})
			if err != nil || len(r.Changes) != 2 || r.Changes[0].Status != "rejected" || r.Changes[1].Status != "saved" {
				t.Fatal(r, err)
			}
			if reason := r.Changes[0].Operations[0].Reason; reason == nil || reason.Code != code {
				t.Fatal("wrong real read reason", reason, code)
			}
			if r.Revision != "1" || batchCount(t, s, "mooring_collab_target_effects") != 1 {
				t.Fatal("independent atomic outcome lost", r)
			}
		})
	}
}

type lifecycleTargetAdapter struct{}

func (lifecycleTargetAdapter) ValidateTargets(_ context.Context, b []byte) ([]BatchTargetIdentity, error) {
	var in []BatchTargetIdentity
	err := json.Unmarshal(b, &in)
	return in, err
}
func (lifecycleTargetAdapter) PrepareTargets(_ context.Context, b []byte, c BatchChange, _ BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error) {
	var in []BatchTargetIdentity
	if err := json.Unmarshal(b, &in); err != nil {
		return BatchTargetCandidate{}, nil, err
	}
	out := BatchTargetCandidate{Inventory: in, Effects: []BatchOperationEffects{}}
	for _, op := range c.Operations {
		var payload struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(op.Payload, &payload); err != nil {
			return out, nil, err
		}
		e := BatchTargetEffect{TargetID: "q", StructureChanged: true}
		if payload.Action == "create" {
			e.Created = true
			found := false
			for i, t := range out.Inventory {
				if t.TargetID == "q" {
					out.Inventory[i].Deleted = false
					found = true
				}
			}
			if !found {
				out.Inventory = append(out.Inventory, BatchTargetIdentity{TargetID: "q"})
			}
		} else {
			e.Deleted = true
			for i, t := range out.Inventory {
				if t.TargetID == "q" {
					out.Inventory[i].Deleted = true
				}
			}
		}
		out.Effects = append(out.Effects, BatchOperationEffects{OperationID: op.ID, Targets: []BatchTargetEffect{e}})
	}
	var err error
	out.Checkpoint, err = json.Marshal(out.Inventory)
	return out, nil, err
}
func TestBatchTargetsLifecycleTombstoneAndDeletedRead(t *testing.T) {
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "lifecycle-runtime")
	if err != nil {
		t.Fatal(err)
	}
	s := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: lifecycleTargetAdapter{}, Limits: DefaultBatchTargetLimits()}})
	initial, _ := json.Marshal([]BatchTargetIdentity{{TargetID: "p"}})
	if err = s.Initialize(t.Context(), lease, initial); err != nil {
		t.Fatal(err)
	}
	for i, action := range []string{"create", "delete"} {
		op := testBatchOp(action)
		op.Payload = json.RawMessage(`{"action":"` + action + `"}`)
		change := testBatchChange(t, key, "change-"+action, "", op)
		head := "0"
		if i == 1 {
			head = "1"
		}
		if _, err = s.Commit(t.Context(), BatchRequest{lease, head, []BatchChange{change}}); err != nil {
			t.Fatal(err)
		}
	}
	var revision, structure string
	var deleted bool
	if err = s.db.QueryRow(`SELECT revision,structure_revision,deleted FROM mooring_collab_target_versions WHERE target_id='q'`).Scan(&revision, &structure, &deleted); err != nil || revision != "2" || structure != "2" || !deleted {
		t.Fatal(revision, structure, deleted, err)
	}
	read := testBatchOp("read-deleted")
	read.Reads = []BatchReadPrecondition{{TargetID: "q", Revision: "2"}}
	change := testBatchChange(t, key, "read-tombstone", "", read)
	result, err := s.Commit(t.Context(), BatchRequest{lease, "2", []BatchChange{change}})
	if err != nil || result.Changes[0].Operations[0].Reason.Code != "target_deleted" {
		t.Fatal(result, err)
	}
	revive := testBatchOp("revive")
	revive.Payload = json.RawMessage(`{"action":"create"}`)
	change = testBatchChange(t, key, "revive", "", revive)
	if _, err = s.Commit(t.Context(), BatchRequest{lease, "2", []BatchChange{change}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("retired ID reused", err)
	}
	if batchCount(t, s, "mooring_collab_target_effects") != 2 {
		t.Fatal("retirement history altered")
	}
	var before sql.NullString
	if err = s.db.QueryRow(`SELECT before_revision FROM mooring_collab_target_effects WHERE op_id='create'`).Scan(&before); err != nil || before.Valid {
		t.Fatal("created target invented confirmed revision", before, err)
	}
}

func TestBatchTargetsProvenanceByteQuotaExactAndRollback(t *testing.T) {
	for _, mode := range []string{"exact", "one_under", "known_dependencies", "later_change"} {
		t.Run(mode, func(t *testing.T) {
			s, a, lease := targetFixture(t)
			op := targetReadOp("a", "0")
			c := testBatchChange(t, lease.Key, "bytes-first", "", op)
			row := preparedTargetRow{Before: &BatchTargetVersion{TargetID: "p", Revision: "0", StructureRevision: "0"}, After: BatchTargetVersion{TargetID: "p", Revision: "1", StructureRevision: "0"}, Effect: BatchTargetEffect{TargetID: "p", ContentChanged: true}, OperationID: "a", Revision: "1"}
			record, err := targetEffectRecord(lease.Key, row)
			if err != nil {
				t.Fatal(err)
			}
			need := int64(len(record) + 2)
			s.config.Targets.Limits.ProvenanceBytes = need
			changes := []BatchChange{c}
			if mode == "one_under" {
				s.config.Targets.Limits.ProvenanceBytes = need - 1
			}
			if mode == "known_dependencies" {
				s.config.Targets.Limits.ProvenanceBytes = 1
			}
			if mode == "later_change" {
				s.config.Targets.Limits.ProvenanceBytes = need + 2
				changes = append(changes, testBatchChange(t, lease.Key, "bytes-second", "", testBatchOp("b")))
			}
			events := 0
			s.config.AfterCommit = func(BatchCommitEvent) { events++ }
			r, err := s.Commit(t.Context(), BatchRequest{lease, "0", changes})
			if mode == "exact" {
				if err != nil || r.Revision != "1" || events != 1 {
					t.Fatal("exact budget failed", r, events, err)
				}
				var actual int64
				if err = s.db.QueryRow(`SELECT length(CAST(record_json AS BLOB)) FROM mooring_collab_target_effects`).Scan(&actual); err != nil || actual+2 != need {
					t.Fatal("logical serialized accounting drift", actual, need, err)
				}
				if _, err = s.db.Exec(`UPDATE mooring_collab_target_effects SET record_json='{}'`); err == nil {
					t.Fatal("short accounting record hid indexed effect columns")
				}
				s.config.Targets.Limits.ProvenanceBytes = 1
				if retry, err := s.Commit(t.Context(), BatchRequest{lease, "0", changes}); err != nil || retry.Revision != "1" || events != 1 || a.calls != 1 {
					t.Fatal("lowered quota hid immutable known outcome", retry, err, events, a.calls)
				}
				return
			}
			if !errors.Is(err, ErrBatchCapacity) || len(r.Changes) != 0 || events != 0 {
				t.Fatal("capacity became terminal/partial", r, events, err)
			}
			if mode == "known_dependencies" && a.calls != 0 {
				t.Fatal("known oversized dependencies reached app")
			}
			if mode == "later_change" && a.calls != 2 {
				t.Fatal("did not exercise rollback after prior prepared admission", a.calls)
			}
			if batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_target_effects") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 {
				t.Fatal("capacity failure retained partial history")
			}
			var head, checkpoint string
			if err = s.db.QueryRow(`SELECT revision,checkpoint_base64 FROM mooring_collab_documents`).Scan(&head, &checkpoint); err != nil || head != "0" || checkpoint != "MA==" {
				t.Fatal("capacity failure changed published checkpoint", head, checkpoint, err)
			}
		})
	}
}
