//go:build linux && mooring_rust_integration

package mooring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Explicit real process + SQL qualification. The application is real; current
// participant authority and owner clock are explicit test mocks. This creates
// no listener, live human grant or participant delivery/convergence claim.
func TestRPCCausalActualRustThousandTaskAndRecovery(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("explicit integration requires %s", name)
		}
		return v
	}
	binary := required("MOORING_TEST_RUST_BINARY")
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binaryBytes)
	if hex.EncodeToString(digest[:]) != required("MOORING_TEST_RUST_SHA256") {
		t.Fatal("actual Rust binary digest mismatch")
	}
	source := required("MOORING_TEST_RUST_SOURCE")
	if decoded, e := hex.DecodeString(source); e != nil || len(decoded) != 20 {
		t.Fatal("explicit source provenance required")
	}
	initial, err := os.ReadFile(required("MOORING_TEST_WORDCRAFT_CHECKPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	target := required("MOORING_TEST_WORDCRAFT_TARGET_ID")
	output := required("MOORING_TEST_OUTPUT_DIRECTORY")
	if info, e := os.Stat(output); e != nil || !info.IsDir() {
		t.Fatal("output directory must exist")
	}
	owners, db, _, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	lease, err := owners.Acquire(t.Context(), key, "actual-causal-rust")
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig := RuntimeConfig{Enabled: true, Binary: binary, Versions: causalPrepareVersions, ControlProfile: PrivateCausalPrepareProfile}
	runtime, err := StartRuntime(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	adapter, err := NewCausalRPCPrepareAdapter(runtime, key, causalPrepareVersions)
	if err != nil {
		t.Fatal(err)
	}
	config := BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget(), Adapter: adapter, Limits: DefaultBatchTargetLimits()}}
	store := NewBatchStore(db.DB, config)
	if err = store.Initialize(t.Context(), lease, initial); err != nil {
		t.Fatal("actual guarded model2 checkpoint", err)
	}
	if err = store.RegisterTask(t.Context(), lease, "actual-thousand-task", "person", true); err != nil {
		t.Fatal(err)
	}
	events := 0
	store.config.AfterCommit = func(BatchCommitEvent) { events++ }
	config.AfterCommit = store.config.AfterCommit
	requests := []BatchRequest{}
	results := []BatchResult{}
	fingerprints := map[string]string{}
	durations := []int64{}
	start := time.Now()
	for offset := 0; offset < 1000; offset += 250 {
		operations := make([]BatchOperation, 0, 250)
		zero := "0"
		for index := offset; index < offset+250; index++ {
			id := fmt.Sprintf("causal-task-op-%04d", index)
			op := testBatchOp(id)
			op.BaseRevision = "0"
			op.Reads = []BatchReadPrecondition{{TargetID: target, Revision: "0", StructureRevision: &zero}}
			if index > 0 {
				op.Dependencies = []string{fmt.Sprintf("causal-task-op-%04d", index-1)}
			}
			op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": target, "at": map[string]any{"kind": "end"}, "text": fmt.Sprintf(" causal-%04d😀", index), "properties": map[string]bool{"bold": index%2 == 0, "italic": index%3 == 0}})
			operations = append(operations, op)
		}
		change := testBatchChange(t, key, fmt.Sprintf("causal-task-change-%d", offset), "actual-thousand-task", operations...)
		for _, op := range change.Operations {
			fingerprints[op.ID] = op.Fingerprint
		}
		request := BatchRequest{lease, strconv.Itoa(offset), []BatchChange{change}}
		before, _ := json.Marshal(request)
		batchStart := time.Now()
		result, e := store.Commit(t.Context(), request)
		durations = append(durations, time.Since(batchStart).Milliseconds())
		if e != nil || result.Revision != strconv.Itoa(offset+250) || len(result.Changes) != 1 || result.Changes[0].Status != "saved" || len(result.Changes[0].Operations) != 250 {
			t.Fatalf("actual causal batch%d result%+v err%v", offset, result, e)
		}
		after, _ := json.Marshal(request)
		if !bytes.Equal(before, after) {
			t.Fatal("original request was rewritten")
		}
		for index, op := range result.Changes[0].Operations {
			if op.Status != "saved" || op.Revision != strconv.Itoa(offset+index+1) || op.Issuer != "crewship" || op.Durability != "node" {
				t.Fatal("noncontinuous host receipt", op)
			}
		}
		requests = append(requests, request)
		results = append(results, result)
		t.Logf("actual causal batch offset=%d members=250 elapsed_ms=%d", offset, durations[len(durations)-1])
	}
	elapsed := time.Since(start)
	if events != 4 || batchCount(t, store, "mooring_collab_target_effects") != 1000 || batchCount(t, store, "mooring_collab_operations") != 1000 {
		t.Fatal("incomplete durable task", events)
	}
	var complete int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_operations WHERE target_effects_complete=1 AND status='saved'`).Scan(&complete); err != nil || complete != 1000 {
		t.Fatal("missing durable provenance", complete, err)
	}
	var revision, structure string
	if err = store.db.QueryRow(`SELECT revision,structure_revision FROM mooring_collab_target_versions WHERE page_id=? AND target_id=?`, key.ArtifactID, target).Scan(&revision, &structure); err != nil || revision != "1000" || structure != "0" {
		t.Fatal("host-owned target counters", revision, structure, err)
	}
	head, saved, err := store.ReadCheckpoint(t.Context(), key)
	if err != nil || head != "1000" || bytes.Equal(saved, initial) {
		t.Fatal("final candidate not durable", head, err)
	}
	if _, err = adapter.ValidateTargets(t.Context(), saved); err != nil {
		t.Fatal("actual Rust final checkpoint restore", err)
	}
	_ = runtime.Close()
	recovered := NewBatchStore(db.DB, config)
	statusStart := time.Now()
	for id, fingerprint := range fingerprints {
		status, e := recovered.Status(t.Context(), key, id, fingerprint)
		if e != nil || status == nil || status.Status != "saved" || status.Issuer != "crewship" || status.Durability != "node" {
			t.Fatal("host-only status after Rust death", id, status, e)
		}
	}
	statusElapsed := time.Since(statusStart)
	_, restored, err := recovered.ReadCheckpoint(t.Context(), key)
	if err != nil || !bytes.Equal(saved, restored) {
		t.Fatal("host-only checkpoint recovery", err)
	}
	for index, request := range requests {
		retry, e := recovered.Commit(t.Context(), request)
		if e != nil || retry.Revision != "1000" || !reflect.DeepEqual(retry.Changes, results[index].Changes) || events != 4 {
			t.Fatal("immutable replay changed receipts or reapplied", index, retry, e, events)
		}
	}
	// A fresh actual child restores only the authoritative committed checkpoint.
	fresh, err := StartRuntime(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	freshAdapter, err := NewCausalRPCPrepareAdapter(fresh, key, causalPrepareVersions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = freshAdapter.ValidateTargets(t.Context(), saved); err != nil {
		t.Fatal("fresh actual Rust restore", err)
	}
	if err = os.WriteFile(filepath.Join(output, "saved-checkpoint.json"), saved, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(results)
	if err = os.WriteFile(filepath.Join(output, "host-results.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.MarshalIndent(map[string]any{"native_source": source, "native_binary_sha256": hex.EncodeToString(digest[:]), "versions": causalPrepareVersions, "profile": PrivateCausalPrepareProfile, "operations": 1000, "atomic_members": 250, "commits": 4, "initial_checkpoint_bytes": len(initial), "final_checkpoint_bytes": len(saved), "batch_elapsed_ms": durations, "task_elapsed_ms": elapsed.Milliseconds(), "status_1000_elapsed_ms": statusElapsed.Milliseconds(), "authority_mock": true, "owner_clock_mock": true, "real_participants": false, "engine_restart": true, "host_restart": false, "immutable_retry_no_engine": true, "original_reads_unchanged": true}, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "evidence.json"), evidence, 0600); err != nil {
		t.Fatal(err)
	}
}
