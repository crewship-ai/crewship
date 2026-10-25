package mooring

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func causalQualify(t *testing.T, s *BatchStore, key SessionKey, change BatchChange, budget BatchHistoryBudget) (BatchCausalPrepareContext, error) {
	t.Helper()
	var result BatchCausalPrepareContext
	err := s.transaction(t.Context(), func(tx *sql.Tx) error {
		var err error
		result, err = s.qualifyCausalReads(t.Context(), tx, key, change, []BatchTargetIdentity{{TargetID: "p"}}, budget)
		return err
	})
	return result, err
}
func causalCommit(t *testing.T, s *BatchStore, lease OwnerLease, id, revision string, deps ...string) {
	t.Helper()
	op := targetReadOp(id, revision)
	op.Dependencies = deps
	if op.Dependencies == nil {
		op.Dependencies = []string{}
	}
	change := testBatchChange(t, lease.Key, "change-"+id, "", op)
	result, err := s.Commit(t.Context(), BatchRequest{lease, revision, []BatchChange{change}})
	if err != nil || len(result.Changes) != 1 || result.Changes[0].Status != "saved" {
		t.Fatal("seed host commit", id, result, err)
	}
}
func TestCausalSQLCoveredChainAndEveryInterveningEffect(t *testing.T) {
	s, _, lease := targetFixture(t)
	causalCommit(t, s, lease, "a", "0")
	probe := targetReadOp("probe", "0")
	probe.Dependencies = []string{"a"}
	change := testBatchChange(t, lease.Key, "probe-change", "", probe)
	q, err := causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err != nil || len(q.QualifiedReads) != 1 || !q.QualifiedReads[0].Covered || q.QualifiedReads[0].ObservedRevision != "0" || q.QualifiedReads[0].Entry.Revision != "1" {
		t.Fatal(q, err)
	}
	causalCommit(t, s, lease, "unrelated", "1")
	causalCommit(t, s, lease, "later-covered", "2", "a")
	probe.Dependencies = []string{"later-covered"}
	change = testBatchChange(t, lease.Key, "probe-change", "", probe)
	q, err = causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err != nil || q.QualifiedReads[0].Covered {
		t.Fatal("latest covered effect concealed unrelated middle effect", q, err)
	}
	probe.Dependencies = append(probe.Dependencies, "unrelated")
	change = testBatchChange(t, lease.Key, "probe-change", "", probe)
	q, err = causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err != nil || !q.QualifiedReads[0].Covered {
		t.Fatal("full dependency coverage refused", q, err)
	}
}
func TestCausalSQLCompleteZeroEffectAndVirtualPrefix(t *testing.T) {
	s, a, lease := targetFixture(t)
	causalCommit(t, s, lease, "a", "0")
	a.mutate = func(candidate *BatchTargetCandidate, _ *BatchPrepareContext) {
		candidate.Effects[0].Targets = []BatchTargetEffect{}
	}
	causalCommit(t, s, lease, "connector", "1", "a")
	opA := targetReadOp("virtual-a", "0")
	opA.Dependencies = []string{"connector"}
	opB := targetReadOp("virtual-b", "0")
	opB.Dependencies = []string{"virtual-a"}
	change := testBatchChange(t, lease.Key, "virtual-change", "", opA, opB)
	q, err := causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err != nil || len(q.QualifiedReads) != 2 || !q.QualifiedReads[0].Covered || !q.QualifiedReads[1].Covered {
		t.Fatal(q, err)
	}
	if _, err = s.db.Exec(`UPDATE mooring_collab_operations SET target_effects_complete=0 WHERE op_id='connector'`); err != nil {
		t.Fatal(err)
	}
	q, err = causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err == nil || len(q.QualifiedReads) != 0 {
		t.Fatal("incomplete marker converted to guessed stale rejection", q, err)
	}
}
func TestCausalSQLActualThousandOperationTaskAndBoundedBatch(t *testing.T) {
	s, _, lease := targetFixture(t)
	s.config.Limits.Checkpoints = 1200
	if err := s.RegisterTask(t.Context(), lease, "thousand-task", "person", true); err != nil {
		t.Fatal(err)
	}
	seedStart := time.Now()
	for start := 0; start < 1000; start += 256 {
		end := start + 256
		if end > 1000 {
			end = 1000
		}
		changes := make([]BatchChange, 0, end-start)
		for i := start; i < end; i++ {
			id := fmt.Sprintf("task-op-%04d", i)
			op := targetReadOp(id, strconv.Itoa(i))
			if i > 0 {
				op.Dependencies = []string{fmt.Sprintf("task-op-%04d", i-1)}
			}
			changes = append(changes, testBatchChange(t, lease.Key, "task-change-"+id, "thousand-task", op))
		}
		result, err := s.Commit(t.Context(), BatchRequest{lease, strconv.Itoa(start), changes})
		if err != nil || len(result.Changes) != len(changes) || result.Revision != strconv.Itoa(end) {
			t.Fatal("actual bounded task batch", start, result, err)
		}
		for _, change := range result.Changes {
			if change.Status != "saved" {
				t.Fatal("actual task member rejected", change)
			}
		}
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM mooring_collab_changes WHERE task_id='thousand-task'`).Scan(&count); err != nil || count != 1000 {
		t.Fatal("not a real host task", count, err)
	}
	seedDuration := time.Since(seedStart)
	probe := targetReadOp("after-task", "0")
	probe.Dependencies = []string{"task-op-0999"}
	change := testBatchChange(t, lease.Key, "after-task-change", "", probe)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	q, err := causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil || len(q.QualifiedReads) != 1 || !q.QualifiedReads[0].Covered || q.QualifiedReads[0].Entry.Revision != "1000" {
		t.Fatal("required1k causal chain", q, err)
	}
	raw, err := json.Marshal(q)
	if err != nil || len(raw) > MaxPrivatePrepareRPCFrame {
		t.Fatal("unbounded compact context", len(raw), err)
	}
	t.Logf("actual_task_operations=1000 commits=4 seed=%s qualify=%s compact_bytes=%d total_allocated_bytes=%d", seedDuration, elapsed, len(raw), after.TotalAlloc-before.TotalAlloc)
	operations := make([]BatchOperation, 256)
	for i := range operations {
		operations[i] = targetReadOp(fmt.Sprintf("batch-probe-%d", i), "0")
		operations[i].Dependencies = []string{"task-op-0999"}
	}
	batch := testBatchChange(t, lease.Key, "full-batch-probe", "", operations...)
	full, err := causalQualify(t, s, lease.Key, batch, DefaultBatchHistoryBudget())
	if err != nil || len(full.QualifiedReads) != 256 {
		t.Fatal("full bounded batch lost1k history", len(full.QualifiedReads), err)
	}
	for _, read := range full.QualifiedReads {
		if !read.Covered {
			t.Fatal("full bounded batch uncovered read")
		}
	}
	raw, err = json.Marshal(full)
	if err != nil || len(raw) > MaxPrivatePrepareRPCFrame {
		t.Fatal(len(raw), err)
	}
	t.Logf("full_batch_operations=256 history_operations=1000 compact_bytes=%d", len(raw))
	budget := DefaultBatchHistoryBudget()
	budget.Operations = 999
	exhausted, err := causalQualify(t, s, lease.Key, change, budget)
	if !errors.Is(err, ErrBatchCapacity) || len(exhausted.QualifiedReads) != 0 {
		t.Fatal("capacity truncated proof", exhausted, err)
	}
	causalCommit(t, s, lease, "external-after-task", "1000")
	q, err = causalQualify(t, s, lease.Key, change, DefaultBatchHistoryBudget())
	if err != nil || q.QualifiedReads[0].Covered {
		t.Fatal("independent external effect covered", q, err)
	}
	if batchCount(t, s, "mooring_collab_operations") != 1001 {
		t.Fatal("qualification installed or rewrote operations")
	}
}

func TestCausalSQLCorruptOrOverbudgetHistoryNeverBecomesRejection(t *testing.T) {
	s, _, lease := targetFixture(t)
	causalCommit(t, s, lease, "a", "0")
	op := targetReadOp("probe", "0")
	op.Dependencies = []string{"a"}
	change := testBatchChange(t, lease.Key, "probe-change", "", op)
	for _, mode := range []string{"extra-record-field", "oversized-record", "missing-effect", "incomplete-marker", "extra-outcome-field", "noncanonical-dependencies", "byte-budget", "virtual-id-shadows-saved"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = ownerFence(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
			localChange := change
			savedLimit := s.config.Targets.Limits.ProvenanceBytes
			defer func() { s.config.Targets.Limits.ProvenanceBytes = savedLimit }()
			switch mode {
			case "extra-record-field":
				_, err = tx.Exec(`UPDATE mooring_collab_target_effects SET record_json=json_set(record_json,'$.unexpected',true)`)
			case "oversized-record":
				_, err = tx.Exec(`UPDATE mooring_collab_target_effects SET record_json=json_set(record_json,'$.unexpected',?)`, string(make([]byte, 9000)))
			case "missing-effect":
				_, err = tx.Exec(`DELETE FROM mooring_collab_target_effects`)
			case "incomplete-marker":
				_, err = tx.Exec(`UPDATE mooring_collab_operations SET target_effects_complete=0`)
			case "extra-outcome-field":
				_, err = tx.Exec(`UPDATE mooring_collab_operations SET result_json=json_set(result_json,'$.unexpected',true)`)
			case "noncanonical-dependencies":
				_, err = tx.Exec(`UPDATE mooring_collab_operations SET dependencies_json='[ ]'`)
			case "byte-budget":
				s.config.Targets.Limits.ProvenanceBytes = 1
			case "virtual-id-shadows-saved":
				shadow := targetReadOp("a", "0")
				localChange = testBatchChange(t, lease.Key, "shadow-change", "", shadow)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.qualifyCausalReads(t.Context(), tx, lease.Key, localChange, []BatchTargetIdentity{{TargetID: "p"}}, DefaultBatchHistoryBudget())
			if err == nil || len(result.QualifiedReads) != 0 {
				t.Fatal("corrupt/incomplete proof became read classification", result, err)
			}
		})
	}
}
